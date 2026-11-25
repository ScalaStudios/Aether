package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/scalastudios/aether/internal/config"
	"github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/mail/queue"
	"github.com/scalastudios/aether/internal/secrets"
)

const sessionCookie = "aether_session"

type Server struct {
	cfg           *config.Config
	db            *pgxpool.Pool
	box           *secrets.Box
	kv            *redis.Client
	mail          *mail.Client
	queue         *queue.Client
	log           *slog.Logger
	oidc          *oidcCache
	resolve       resolver
	webhookClient *http.Client
	imports       sync.Map
	importSlots   chan struct{}
	kickWake      chan struct{}
	origin        string
	hub           *liveHub
	notifyWake    chan struct{}
	watchesMu     sync.Mutex
	watches       map[string]context.CancelFunc
	vapidMu       sync.Mutex
	vapidPublic   string
	vapidPrivate  string
	apnsMu        sync.Mutex
	apnsKey       string
	apns          *apnsClient
	scheduleWake  chan struct{}
	imageClient   *http.Client
}

func New(cfg *config.Config, db *pgxpool.Pool, box *secrets.Box, kv *redis.Client, mc *mail.Client, log *slog.Logger) *Server {
	return &Server{cfg: cfg, db: db, box: box, kv: kv, mail: mc, queue: queue.NewClient(cfg.PostfixAPIURL, cfg.PostfixAPIKey), log: log, oidc: newOIDCCache(), resolve: net.DefaultResolver, webhookClient: newWebhookClient(cfg.WebhookAllowPrivate), kickWake: make(chan struct{}, 1), importSlots: make(chan struct{}, 4),
		origin: secrets.Token(6), hub: newLiveHub(), notifyWake: make(chan struct{}, 1), scheduleWake: make(chan struct{}, 1), imageClient: newImageClient(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/image-proxy/{token}", s.proxyImage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.ready)

	mux.HandleFunc("POST /api/alias/random/new", s.generateMaskedAlias)
	mux.HandleFunc("OPTIONS /api/alias/random/new", s.generateMaskedAlias)
	mux.HandleFunc("GET /api/v1/files/{token}", s.getFileLinkMeta)
	mux.HandleFunc("GET /api/v1/files/{token}/content", s.downloadFileLink)
	mux.HandleFunc("POST /api/v1/files/{token}/content", s.downloadFileLink)
	mux.HandleFunc("GET /api/v1/auth/providers", s.authProviders)
	mux.HandleFunc("GET /api/v1/branding", s.currentBranding)
	mux.HandleFunc("GET /api/v1/branding/{orgId}/{asset}", s.brandAsset)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/protected/{messageId}/open", s.protectedRecipient)
	mux.HandleFunc("POST /api/v1/protected/{messageId}/reply", s.replyProtectedMessage)
	mux.HandleFunc("GET /api/v1/auth/oidc/{slug}/start", s.oidcStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/{slug}/callback", s.oidcCallback)
	mux.HandleFunc("POST /api/v1/hooks/identity-providers/{slug}", s.providerWebhook)
	mux.HandleFunc("GET /api/v1/scim/{slug}/ServiceProviderConfig", s.scimServiceProviderConfig)
	mux.HandleFunc("GET /api/v1/scim/{slug}/Users", s.scimListUsers)
	mux.HandleFunc("POST /api/v1/scim/{slug}/Users", s.scimCreateUser)
	mux.HandleFunc("GET /api/v1/scim/{slug}/Users/{id}", s.scimGetUser)
	mux.HandleFunc("PUT /api/v1/scim/{slug}/Users/{id}", s.scimReplaceUser)
	mux.HandleFunc("PATCH /api/v1/scim/{slug}/Users/{id}", s.scimPatchUser)
	mux.HandleFunc("DELETE /api/v1/scim/{slug}/Users/{id}", s.scimDeleteUser)
	mux.HandleFunc("GET /api/v1/scim/{slug}/Groups", s.scimListGroups)
	mux.HandleFunc("POST /api/v1/scim/{slug}/Groups", s.scimCreateGroup)
	mux.HandleFunc("GET /api/v1/scim/{slug}/Groups/{id}", s.scimGetGroup)
	mux.HandleFunc("PUT /api/v1/scim/{slug}/Groups/{id}", s.scimReplaceGroup)
	mux.HandleFunc("PATCH /api/v1/scim/{slug}/Groups/{id}", s.scimPatchGroup)
	mux.HandleFunc("DELETE /api/v1/scim/{slug}/Groups/{id}", s.scimDeleteGroup)
	mux.HandleFunc("/dav/", s.serveDAV)
	mux.HandleFunc("/.well-known/caldav", s.serveDAV)
	mux.HandleFunc("/.well-known/carddav", s.serveDAV)
	mux.HandleFunc("GET /.well-known/jmap", s.jmapSession)
	mux.HandleFunc("OPTIONS /.well-known/jmap", s.jmapOptions)
	mux.HandleFunc("POST /jmap", s.jmapAPI)
	mux.HandleFunc("OPTIONS /jmap", s.jmapOptions)
	mux.HandleFunc("POST /jmap/upload/{accountId}", s.jmapUpload)
	mux.HandleFunc("OPTIONS /jmap/upload/{accountId}", s.jmapOptions)
	mux.HandleFunc("GET /jmap/download/{accountId}/{blobId}/{name}", s.jmapDownload)
	mux.HandleFunc("OPTIONS /jmap/download/{accountId}/{blobId}/{name}", s.jmapOptions)
	mux.HandleFunc("GET /jmap/eventsource", s.jmapEvents)
	mux.HandleFunc("OPTIONS /jmap/eventsource", s.jmapOptions)
	mux.HandleFunc("GET /.well-known/mta-sts.txt", s.mtaSTSPolicyFile)
	mux.HandleFunc("GET /internal/tls-ask", s.tlsAsk)
	mux.HandleFunc("POST /internal/mail-token", s.verifyMailToken)
	mux.HandleFunc("POST /internal/quarantine", s.quarantineIngest)
	mux.HandleFunc("POST /internal/sender-lists/check", s.checkSenderLists)
	mux.HandleFunc("POST /internal/spam-learn", s.learnSpam)
	mux.HandleFunc("POST /internal/trusted-sender", s.trustedSender)
	mux.HandleFunc("POST /internal/mail-events", s.receiveMailEvent)
	mux.HandleFunc("POST /internal/auth-policy", s.authPolicy)
	mux.HandleFunc("POST /internal/ios-push", s.registerIOSPush)
	mux.HandleFunc("DELETE /internal/ios-push", s.deleteIOSPush)
	mux.HandleFunc("GET /mail/config-v1.1.xml", s.mailAutoconfig)
	mux.HandleFunc("GET /.well-known/autoconfig/mail/config-v1.1.xml", s.mailAutoconfig)
	mux.HandleFunc("/autodiscover/autodiscover.xml", s.mailAutodiscover)
	mux.HandleFunc("/Autodiscover/Autodiscover.xml", s.mailAutodiscover)

	mux.HandleFunc("POST /api/v1/auth/login/two-factor", s.loginTwoFactor)

	authed := func(pattern string, h func(http.ResponseWriter, *http.Request, *principal)) {
		mux.HandleFunc(pattern, s.withPrincipal(h, false))
	}
	duringSetup := func(pattern string, h func(http.ResponseWriter, *http.Request, *principal)) {
		mux.HandleFunc(pattern, s.withPrincipal(h, true))
	}
	duringSetup("POST /api/v1/auth/logout", s.logout)
	duringSetup("GET /api/v1/session", s.session)
	duringSetup("GET /api/v1/me/security", s.securityStatus)
	duringSetup("PUT /api/v1/me/password", s.changePassword)
	duringSetup("POST /api/v1/me/totp", s.startTOTP)
	duringSetup("PUT /api/v1/me/totp", s.confirmTOTP)
	authed("DELETE /api/v1/me/totp", s.deleteTOTP)
	duringSetup("POST /api/v1/me/security-keys/challenge", s.securityKeyChallenge)
	duringSetup("POST /api/v1/me/security-keys", s.addSecurityKey)
	authed("DELETE /api/v1/me/security-keys/{id}", s.deleteSecurityKey)
	authed("POST /api/v1/me/recovery-codes", s.regenerateRecoveryCodes)
	duringSetup("GET /api/v1/me/sessions", s.listMySessions)
	duringSetup("DELETE /api/v1/me/sessions", s.endMySessions)
	duringSetup("DELETE /api/v1/me/sessions/{sessionId}", s.endMySessions)
	authed("GET /api/v1/admin/outbound-routing", s.listOutboundRoutes)
	authed("PUT /api/v1/admin/outbound-routing/{kind}/{name}", s.saveOutboundRoute)
	authed("DELETE /api/v1/admin/outbound-routing/{kind}/{name}", s.deleteOutboundRoute)

	authed("GET /api/v1/organizations", s.listOrganizations)
	authed("POST /api/v1/organizations", s.createOrganization)
	authed("GET /api/v1/organizations/{orgId}", s.getOrganization)
	authed("PATCH /api/v1/organizations/{orgId}", s.updateOrganization)
	authed("GET /api/v1/organizations/{orgId}/encryption", s.encryptionSettings)
	authed("PUT /api/v1/organizations/{orgId}/encryption", s.encryptionSettings)
	authed("GET /api/v1/organizations/{orgId}/mail-privacy", s.getMailPrivacy)
	authed("PUT /api/v1/organizations/{orgId}/mail-privacy", s.saveMailPrivacy)
	authed("GET /api/v1/organizations/{orgId}/transfer-limits", s.getTransferLimits)
	authed("PUT /api/v1/organizations/{orgId}/transfer-limits", s.saveTransferLimits)
	authed("GET /api/v1/organizations/{orgId}/file-links", s.listOrgFileLinks)
	authed("DELETE /api/v1/organizations/{orgId}/file-links/{linkId}", s.deleteOrgFileLink)
	authed("GET /api/v1/organizations/{orgId}/branding", s.getBranding)
	authed("PUT /api/v1/organizations/{orgId}/branding", s.saveBranding)
	authed("PUT /api/v1/organizations/{orgId}/branding/{image}", s.putBrandImage)
	authed("DELETE /api/v1/organizations/{orgId}/branding/{image}", s.deleteBrandImage)
	authed("GET /api/v1/organizations/{orgId}/quota-warnings", s.getQuotaWarnings)
	authed("PUT /api/v1/organizations/{orgId}/quota-warnings", s.saveQuotaWarnings)
	authed("GET /api/v1/organizations/{orgId}/sender-warnings", s.getSenderWarnings)
	authed("PUT /api/v1/organizations/{orgId}/sender-warnings", s.saveSenderWarnings)
	authed("GET /api/v1/organizations/{orgId}/attachment-policy", s.getAttachmentPolicy)
	authed("PUT /api/v1/organizations/{orgId}/attachment-policy", s.saveAttachmentPolicy)
	authed("GET /api/v1/organizations/{orgId}/signature-templates", s.listSignatureTemplates)
	authed("PUT /api/v1/organizations/{orgId}/signature-templates", s.saveSignatureTemplate)
	authed("DELETE /api/v1/organizations/{orgId}/signature-templates", s.deleteSignatureTemplate)
	authed("POST /api/v1/organizations/{orgId}/signature-templates/preview", s.previewSignatureTemplate)
	authed("GET /api/v1/organizations/{orgId}/sender-lists", s.listOrgSenderLists)
	authed("POST /api/v1/organizations/{orgId}/sender-lists", s.addOrgSenderEntry)
	authed("DELETE /api/v1/organizations/{orgId}/sender-lists/{entryId}", s.removeOrgSenderEntry)
	authed("GET /api/v1/organizations/{orgId}/mail-flow/rules", s.listFlowRules)
	authed("POST /api/v1/organizations/{orgId}/mail-flow/rules", s.createFlowRule)
	authed("PUT /api/v1/organizations/{orgId}/mail-flow/rules/{ruleId}", s.updateFlowRule)
	authed("DELETE /api/v1/organizations/{orgId}/mail-flow/rules/{ruleId}", s.deleteFlowRule)
	authed("PUT /api/v1/organizations/{orgId}/mail-flow/order", s.orderFlowRules)
	authed("GET /api/v1/organizations/{orgId}/mail-flow/matches", s.listFlowMatches)
	authed("GET /api/v1/organizations/{orgId}/mail-flow/held", s.listOrgHeld)
	authed("POST /api/v1/mail-flow/held/{heldId}/approve", s.approveHeld)
	authed("POST /api/v1/mail-flow/held/{heldId}/reject", s.rejectHeld)
	authed("GET /api/v1/me/mail-flow/held", s.myHeld)
	authed("GET /api/v1/organizations/{orgId}/forwarding", s.getForwarding)
	authed("PUT /api/v1/organizations/{orgId}/forwarding", s.saveForwarding)
	authed("GET /api/v1/organizations/{orgId}/retention-policies", s.listRetentionPolicies)
	authed("POST /api/v1/organizations/{orgId}/retention-policies", s.createRetentionPolicy)
	authed("PATCH /api/v1/retention-policies/{policyId}", s.updateRetentionPolicy)
	authed("DELETE /api/v1/retention-policies/{policyId}", s.deleteRetentionPolicy)
	authed("GET /api/v1/organizations/{orgId}/members", s.listMembers)
	authed("POST /api/v1/organizations/{orgId}/members", s.setMember)
	authed("DELETE /api/v1/organizations/{orgId}/members/{userId}", s.removeMember)
	authed("GET /api/v1/organizations/{orgId}/members/{userId}/sessions", s.listMemberSessions)
	authed("DELETE /api/v1/organizations/{orgId}/members/{userId}/sessions", s.endMemberSessions)
	authed("DELETE /api/v1/organizations/{orgId}/members/{userId}/sessions/{sessionId}", s.endMemberSessions)
	authed("GET /api/v1/organizations/{orgId}/security", s.getOrgSecurity)
	authed("PUT /api/v1/organizations/{orgId}/security", s.saveOrgSecurity)
	authed("GET /api/v1/organizations/{orgId}/audit", s.listAudit)
	authed("GET /api/v1/organizations/{orgId}/quarantine", s.listOrgQuarantine)
	authed("GET /api/v1/organizations/{orgId}/quarantine/{id}", s.getOrgQuarantine)
	authed("POST /api/v1/organizations/{orgId}/quarantine/release", s.releaseOrgQuarantine)
	authed("POST /api/v1/organizations/{orgId}/quarantine/delete", s.deleteOrgQuarantine)
	authed("GET /api/v1/organizations/{orgId}/quarantine-policy", s.getQuarantinePolicy)
	authed("PUT /api/v1/organizations/{orgId}/quarantine-policy", s.saveQuarantinePolicy)
	authed("GET /api/v1/organizations/{orgId}/message-trace", s.listMessageTrace)
	authed("GET /api/v1/organizations/{orgId}/message-trace/export", s.exportMessageTrace)
	authed("GET /api/v1/organizations/{orgId}/message-trace/{id}", s.getMessageTrace)
	authed("GET /api/v1/organizations/{orgId}/mailbox-audit", s.getMailboxAudit)
	authed("PUT /api/v1/organizations/{orgId}/mailbox-audit", s.saveMailboxAudit)
	authed("GET /api/v1/organizations/{orgId}/api-keys", s.listAPIKeys)
	authed("POST /api/v1/organizations/{orgId}/api-keys", s.createAPIKey)
	authed("DELETE /api/v1/api-keys/{keyId}", s.revokeAPIKey)
	authed("GET /api/v1/organizations/{orgId}/sending-limits", s.getSendingLimits)
	authed("PUT /api/v1/organizations/{orgId}/sending-limits", s.saveSendingLimits)
	authed("GET /api/v1/organizations/{orgId}/restricted-senders", s.listRestrictedSenders)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/sending-restriction", s.releaseSender)
	authed("GET /api/v1/organizations/{orgId}/webhooks", s.listWebhooks)
	authed("POST /api/v1/organizations/{orgId}/webhooks", s.createWebhook)
	authed("DELETE /api/v1/webhooks/{webhookId}", s.deleteWebhook)
	authed("GET /api/v1/webhooks/{webhookId}/deliveries", s.listDeliveries)
	authed("POST /api/v1/webhooks/{webhookId}/test", s.testWebhook)
	authed("GET /api/v1/organizations/{orgId}/suppressions", s.listSuppressions)
	authed("POST /api/v1/organizations/{orgId}/suppressions", s.createSuppression)
	authed("DELETE /api/v1/suppressions/{suppressionId}", s.deleteSuppression)

	authed("GET /api/v1/organizations/{orgId}/domains", s.listDomains)
	authed("POST /api/v1/organizations/{orgId}/domains", s.createDomain)
	authed("GET /api/v1/domains/{domainId}", s.getDomain)
	authed("PATCH /api/v1/domains/{domainId}", s.updateDomain)
	authed("PUT /api/v1/domains/{domainId}/coexistence", s.setDomainCoexistence)
	authed("DELETE /api/v1/domains/{domainId}", s.deleteDomain)
	authed("GET /api/v1/domains/{domainId}/dns", s.domainDNS)
	authed("POST /api/v1/domains/{domainId}/verify", s.verifyDomain)
	authed("GET /api/v1/domains/{domainId}/dmarc", s.domainDMARC)
	authed("GET /api/v1/domains/{domainId}/dkim", s.listDKIMKeys)
	authed("POST /api/v1/domains/{domainId}/dkim", s.createDKIMKey)
	authed("POST /api/v1/domains/{domainId}/dkim/{keyId}/activate", s.activateDKIMKey)
	authed("DELETE /api/v1/domains/{domainId}/dkim/{keyId}", s.deleteDKIMKey)

	authed("GET /api/v1/organizations/{orgId}/mailboxes", s.listMailboxes)
	authed("POST /api/v1/organizations/{orgId}/mailboxes", s.createMailbox)
	authed("PATCH /api/v1/mailboxes/{mailboxId}", s.updateMailbox)
	authed("DELETE /api/v1/mailboxes/{mailboxId}", s.deleteMailbox)
	authed("POST /api/v1/mailboxes/{mailboxId}/convert", s.convertMailbox)
	authed("PUT /api/v1/mailboxes/{mailboxId}/resource", s.updateResource)
	authed("GET /api/v1/organizations/{orgId}/resources", s.listResources)
	authed("GET /api/v1/mailboxes/{mailboxId}/delegates", s.listDelegates)
	authed("POST /api/v1/mailboxes/{mailboxId}/delegates", s.addDelegate)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/delegates/{userId}", s.removeDelegate)
	authed("GET /api/v1/mailboxes/{mailboxId}/access-log", s.mailboxAccessLog)

	authed("GET /api/v1/organizations/{orgId}/personal-alias-policy", s.getPersonalAliasPolicy)
	authed("PUT /api/v1/organizations/{orgId}/personal-alias-policy", s.setPersonalAliasPolicy)
	authed("GET /api/v1/organizations/{orgId}/personal-aliases", s.listPersonalAliases)
	authed("POST /api/v1/organizations/{orgId}/personal-aliases", s.createPersonalAlias)
	authed("PUT /api/v1/organizations/{orgId}/personal-alias-key", s.setPersonalAliasKey)
	authed("DELETE /api/v1/organizations/{orgId}/personal-alias-key", s.revokePersonalAliasKey)
	authed("GET /api/v1/organizations/{orgId}/aliases", s.listAliases)
	authed("POST /api/v1/organizations/{orgId}/aliases", s.createAlias)
	authed("GET /api/v1/aliases/{aliasId}", s.getAlias)
	authed("PATCH /api/v1/aliases/{aliasId}", s.updateAlias)
	authed("DELETE /api/v1/aliases/{aliasId}", s.deleteAlias)
	authed("GET /api/v1/aliases/{aliasId}/members", s.listResolvedMembers)
	authed("GET /api/v1/organizations/{orgId}/directory-groups", s.listDirectoryGroups)
	authed("GET /api/v1/me/lists", s.myLists)
	authed("GET /api/v1/me/held-messages", s.myHeldMessages)
	authed("GET /api/v1/held-messages/{heldId}/raw", s.heldMessageRaw)
	authed("POST /api/v1/held-messages/{heldId}/{decision}", s.decideHeldMessage)
	authed("GET /api/v1/organizations/{orgId}/contacts", s.listOrgContacts)
	authed("POST /api/v1/organizations/{orgId}/contacts", s.saveOrgContact)
	authed("PUT /api/v1/organizations/{orgId}/contacts/{id}", s.saveOrgContact)
	authed("DELETE /api/v1/organizations/{orgId}/contacts/{id}", s.deleteOrgContact)

	authed("GET /api/v1/me/encryption-keys", s.listEncryptionKeys)
	authed("PUT /api/v1/me/encryption-keys", s.saveEncryptionKey)
	authed("DELETE /api/v1/me/encryption-keys/{keyId}", s.deleteEncryptionKey)
	authed("GET /api/v1/me/recipient-keys", s.recipientKeys)
	authed("POST /api/v1/me/recipient-keys", s.savePeerKey)
	authed("GET /api/v1/me/wkd", s.webKeyDirectory)
	authed("GET /api/v1/events", s.events)
	authed("GET /api/v1/me/notifications", s.getNotifications)
	authed("PUT /api/v1/me/notifications", s.saveNotifications)
	authed("PUT /api/v1/me/live-focus", s.focusMailbox)
	authed("POST /api/v1/me/push-subscriptions", s.createPushSubscription)
	authed("DELETE /api/v1/me/push-subscriptions", s.deletePushSubscription)
	authed("GET /api/v1/me/mailboxes", s.myMailboxes)
	authed("GET /api/v1/me/remote-image-senders", s.listRemoteImageSenders)
	authed("DELETE /api/v1/me/remote-image-senders/{address}", s.forgetRemoteImageSender)
	authed("GET /api/v1/me/app-passwords", s.listAppPasswords)
	authed("POST /api/v1/me/app-passwords", s.createAppPassword)
	authed("DELETE /api/v1/me/app-passwords/{id}", s.revokeAppPassword)
	authed("POST /api/v1/me/apple-profile", s.appleProfile)
	authed("GET /api/v1/mailboxes/{mailboxId}/imports", s.listImports)
	authed("POST /api/v1/mailboxes/{mailboxId}/imports", s.startImport)
	authed("POST /api/v1/mailboxes/{mailboxId}/imports/{id}/cancel", s.cancelImport)
	authed("GET /api/v1/mailboxes/{mailboxId}/import-schedules", s.listMailboxImportSchedules)
	authed("POST /api/v1/mailboxes/{mailboxId}/import-schedules", s.startMailboxImportSchedule)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/import-schedules/{id}", s.stopMailboxImport)
	authed("GET /api/v1/organizations/{orgId}/import-schedules", s.listOrgImportSchedules)
	authed("POST /api/v1/organizations/{orgId}/import-schedules", s.startBulkImports)
	authed("POST /api/v1/organizations/{orgId}/import-schedules/stop", s.stopBulkImports)
	authed("GET /api/v1/organizations/{orgId}/import-schedules/{id}/runs", s.orgImportRuns)
	authed("PUT /api/v1/me/avatar", s.putAvatar)
	authed("PUT /api/v1/me/profile", s.updateProfile)
	authed("DELETE /api/v1/me/avatar", s.deleteAvatar)
	authed("GET /api/v1/avatars/{address}", s.getAvatar)

	authed("GET /api/v1/mailboxes/{mailboxId}/folders", s.listFolders)
	authed("POST /api/v1/mailboxes/{mailboxId}/folders", s.createFolder)
	authed("PATCH /api/v1/mailboxes/{mailboxId}/folders", s.updateFolder)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/folders", s.deleteFolder)
	authed("POST /api/v1/mailboxes/{mailboxId}/folders/empty", s.emptyFolder)
	authed("POST /api/v1/mailboxes/{mailboxId}/folders/mark-read", s.markFolderRead)
	authed("GET /api/v1/mailboxes/{mailboxId}/retention", s.getMailboxRetention)
	authed("GET /api/v1/mailboxes/{mailboxId}/recoverable", s.listRecoverable)
	authed("POST /api/v1/mailboxes/{mailboxId}/recoverable/restore", s.restoreRecoverable)
	authed("GET /api/v1/mailboxes/{mailboxId}/labels", s.listLabels)
	authed("POST /api/v1/mailboxes/{mailboxId}/labels", s.createLabel)
	authed("PATCH /api/v1/mailboxes/{mailboxId}/labels/{labelId}", s.updateLabel)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/labels/{labelId}", s.deleteLabel)
	authed("GET /api/v1/mailboxes/{mailboxId}/saved-searches", s.listSavedSearches)
	authed("POST /api/v1/mailboxes/{mailboxId}/saved-searches", s.createSavedSearch)
	authed("PATCH /api/v1/mailboxes/{mailboxId}/saved-searches/{searchId}", s.updateSavedSearch)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/saved-searches/{searchId}", s.deleteSavedSearch)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages", s.listMessages)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages/{uid}", s.getMessage)
	authed("PATCH /api/v1/mailboxes/{mailboxId}/messages/{uid}", s.updateMessage)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/messages/{uid}", s.deleteMessage)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/move", s.moveMessages)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/copy", s.copyMessages)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/move", s.moveMessages)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/copy", s.copyMessages)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/invitation", s.respondToInvitation)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/unsubscribe", s.unsubscribeMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/newsletters", s.listNewsletters)
	authed("POST /api/v1/mailboxes/{mailboxId}/newsletters/block", s.blockNewsletter)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages/{uid}/attachments/{part}", s.getAttachment)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages/{uid}/source", s.messageSource)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/remote-images", s.setRemoteImages)
	authed("GET /api/v1/mailboxes/{mailboxId}/transfer-limits", s.mailboxTransferLimits)
	authed("POST /api/v1/mailboxes/{mailboxId}/file-links", s.uploadFileLink)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/file-links/{linkId}", s.deleteMailboxFileLink)
	authed("POST /api/v1/mailboxes/{mailboxId}/send", s.sendMessage)
	authed("POST /api/v1/mailboxes/{mailboxId}/protected", s.sendProtectedMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/protected/{messageId}", s.protectedSender)
	authed("PUT /api/v1/mailboxes/{mailboxId}/drafts", s.saveDraft)
	authed("GET /api/v1/mailboxes/{mailboxId}/scheduled", s.listScheduled)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/scheduled/{scheduledId}", s.cancelScheduled)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/snooze", s.snoozeMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/snoozed", s.listSnoozed)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/snoozed/{uid}", s.unsnoozeMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/rules", s.listRules)
	authed("PUT /api/v1/mailboxes/{mailboxId}/rules", s.saveRules)
	authed("GET /api/v1/mailboxes/{mailboxId}/auto-reply", s.getAutoReply)
	authed("PUT /api/v1/mailboxes/{mailboxId}/auto-reply", s.saveAutoReply)
	authed("GET /api/v1/mailboxes/{mailboxId}/tagged-mail", s.getTaggedMail)
	authed("PUT /api/v1/mailboxes/{mailboxId}/tagged-mail", s.saveTaggedMail)
	authed("GET /api/v1/mailboxes/{mailboxId}/signatures", s.getSignatures)
	authed("PUT /api/v1/mailboxes/{mailboxId}/signatures", s.saveSignatures)
	authed("GET /api/v1/mailboxes/{mailboxId}/signature-template", s.mailboxSignatureTemplate)
	authed("GET /api/v1/mailboxes/{mailboxId}/sender-lists", s.listMailboxSenderLists)
	authed("POST /api/v1/mailboxes/{mailboxId}/sender-lists", s.addMailboxSenderEntry)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/sender-lists/{entryId}", s.removeMailboxSenderEntry)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/drafts/{uid}", s.deleteDraft)
	authed("GET /api/v1/mailboxes/{mailboxId}/quarantine", s.listMailboxQuarantine)
	authed("GET /api/v1/mailboxes/{mailboxId}/quarantine/{id}", s.getMailboxQuarantine)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/quarantine/{id}", s.deleteMailboxQuarantine)
	authed("POST /api/v1/mailboxes/{mailboxId}/quarantine/{id}/release", s.releaseMailboxQuarantine)
	authed("POST /api/v1/mailboxes/{mailboxId}/quarantine/{id}/request", s.requestQuarantineRelease)
	authed("POST /api/v1/mailboxes/{mailboxId}/quarantine/{id}/allow-sender", s.allowQuarantineSender)
	authed("GET /api/v1/mailboxes/{mailboxId}/allowed-senders", s.listAllowedSenders)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/allowed-senders", s.removeAllowedSender)

	authed("GET /api/v1/contacts", s.listContacts)
	authed("POST /api/v1/contacts", s.saveContact)
	authed("PUT /api/v1/contacts/{id}", s.saveContact)
	authed("DELETE /api/v1/contacts/{id}", s.deleteContact)
	authed("GET /api/v1/contacts/books", s.listAddressBooks)
	authed("POST /api/v1/contacts/books", s.createAddressBook)
	authed("PATCH /api/v1/contacts/books/{bookId}", s.updateAddressBook)
	authed("DELETE /api/v1/contacts/books/{bookId}", s.deleteAddressBook)
	authed("POST /api/v1/contacts/books/{bookId}/shares", s.shareAddressBook)
	authed("DELETE /api/v1/contacts/books/{bookId}/shares/{shareId}", s.unshareAddressBook)
	authed("GET /api/v1/contacts/groups", s.listContactGroups)
	authed("POST /api/v1/contacts/groups", s.saveContactGroup)
	authed("GET /api/v1/contacts/groups/{id}", s.getContactGroup)
	authed("PUT /api/v1/contacts/groups/{id}", s.saveContactGroup)
	authed("DELETE /api/v1/contacts/groups/{id}", s.deleteContactGroup)
	authed("GET /api/v1/directory", s.listDirectory)
	authed("GET /api/v1/recipients", s.suggestRecipients)
	authed("GET /api/v1/calendar/events", s.listEvents)
	authed("GET /api/v1/calendar/export", s.exportCalendar)
	authed("POST /api/v1/calendar/import", s.importCalendar)
	authed("POST /api/v1/contacts/import", s.importContacts)
	authed("POST /api/v1/mailboxes/{mailboxId}/calendar/import", s.importMailboxCalendar)
	authed("POST /api/v1/mailboxes/{mailboxId}/contacts/import", s.importMailboxContacts)
	authed("POST /api/v1/calendar/events", s.saveEvent)
	authed("PUT /api/v1/calendar/events/{id}", s.saveEvent)
	authed("DELETE /api/v1/calendar/events/{id}", s.deleteEvent)
	authed("GET /api/v1/calendar/freebusy", s.freeBusy)
	authed("GET /api/v1/calendar/resources", s.findResources)
	authed("GET /api/v1/calendar/booking-requests", s.listBookingRequests)
	authed("POST /api/v1/calendar/booking-requests/{requestId}", s.decideBookingRequest)
	authed("GET /api/v1/calendar/calendars", s.listCalendars)
	authed("POST /api/v1/calendar/calendars", s.createCalendar)
	authed("PATCH /api/v1/calendar/calendars/{calendarId}", s.updateCalendar)
	authed("DELETE /api/v1/calendar/calendars/{calendarId}", s.deleteCalendar)
	authed("POST /api/v1/calendar/calendars/{calendarId}/shares", s.shareCalendar)
	authed("DELETE /api/v1/calendar/calendars/{calendarId}/shares/{shareId}", s.unshareCalendar)
	authed("POST /api/v1/calendar/subscriptions", s.createSubscription)
	authed("PATCH /api/v1/calendar/subscriptions/{calendarId}", s.updateSubscription)
	authed("POST /api/v1/calendar/subscriptions/{calendarId}/refresh", s.refreshSubscriptionNow)
	authed("GET /api/v1/calendar/tasks", s.listTasks)
	authed("POST /api/v1/calendar/tasks", s.saveTask)
	authed("PUT /api/v1/calendar/tasks/{id}", s.saveTask)
	authed("DELETE /api/v1/calendar/tasks/{id}", s.deleteTask)
	authed("GET /api/v1/calendar/booking", s.getBookingPage)
	authed("PUT /api/v1/calendar/booking", s.saveBookingPage)
	authed("POST /api/v1/calendar/booking/types", s.createBookingType)
	authed("PUT /api/v1/calendar/booking/types/{typeId}", s.updateBookingType)
	authed("DELETE /api/v1/calendar/booking/types/{typeId}", s.deleteBookingType)
	mux.HandleFunc("GET /api/v1/book/{slug}", s.publicBookingPage)
	mux.HandleFunc("GET /api/v1/book/{slug}/{typeSlug}/slots", s.publicBookingSlots)
	mux.HandleFunc("POST /api/v1/book/{slug}/{typeSlug}", s.bookMeeting)

	authed("GET /api/v1/admin/identity-providers", s.listIdentityProviders)
	authed("POST /api/v1/admin/identity-providers", s.createIdentityProvider)
	authed("POST /api/v1/admin/identity-providers/discover", s.discoverIdentityProvider)
	authed("PUT /api/v1/admin/identity-providers/{id}", s.updateIdentityProvider)
	authed("DELETE /api/v1/admin/identity-providers/{id}", s.deleteIdentityProvider)
	authed("POST /api/v1/admin/identity-providers/{id}/scim-token", s.enableSCIM)
	authed("DELETE /api/v1/admin/identity-providers/{id}/scim-token", s.disableSCIM)
	authed("PATCH /api/v1/admin/users/{id}", s.updateUserStatus)
	authed("GET /api/v1/admin/message-trace", s.listMessageTrace)
	authed("GET /api/v1/admin/message-trace/export", s.exportMessageTrace)
	authed("GET /api/v1/admin/message-trace/{id}", s.getMessageTrace)
	authed("GET /api/v1/admin/mail-queue", s.listMailQueue)
	authed("POST /api/v1/admin/mail-queue/{action}", s.changeMailQueue)

	authed("GET /api/v1/organizations/{orgId}/compliance/cases", s.listCases)
	authed("POST /api/v1/organizations/{orgId}/compliance/cases", s.createCase)
	authed("GET /api/v1/compliance/cases/{caseId}", s.getCase)
	authed("PATCH /api/v1/compliance/cases/{caseId}", s.updateCase)
	authed("POST /api/v1/compliance/cases/{caseId}/search", s.searchCase)
	authed("GET /api/v1/compliance/cases/{caseId}/export", s.exportCase)
	authed("GET /api/v1/compliance/cases/{caseId}/holds", s.listHolds)
	authed("POST /api/v1/compliance/cases/{caseId}/holds", s.addHolds)
	authed("DELETE /api/v1/compliance/cases/{caseId}/holds/{mailboxId}", s.releaseHold)
	authed("GET /api/v1/organizations/{orgId}/compliance/mailboxes", s.complianceMailboxes)
	authed("GET /api/v1/organizations/{orgId}/journal-rules", s.listJournalRules)
	authed("POST /api/v1/organizations/{orgId}/journal-rules", s.createJournalRule)
	authed("DELETE /api/v1/journal-rules/{ruleId}", s.deleteJournalRule)
	authed("PUT /api/v1/admin/users/{id}/password", s.resetUserPassword)
	authed("DELETE /api/v1/admin/users/{id}/two-factor", s.resetUserTwoFactor)
	authed("GET /api/v1/admin/blocked-addresses", s.listBlockedAddresses)
	authed("DELETE /api/v1/admin/blocked-addresses/{id}", s.unblockAddress)

	return instrument(securityHeaders(mux))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.kv.Ping(ctx).Err(); err != nil {
		http.Error(w, "valkey unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type principal struct {
	User           User
	sessionHash    []byte
	csrf           string
	providerID     *string
	idToken        *string
	roles          map[string]string
	compliance     map[string]bool
	apiKey         *apiKeyIdentity
	twoFactorSetup bool
}

func (p *principal) orgRole(orgID string) string {
	if p.User.IsPlatformAdmin {
		return "owner"
	}
	return p.roles[orgID]
}

func (p *principal) canCompliance(orgID string) bool {
	return p.compliance[orgID]
}

func (p *principal) canAdminOrg(orgID string) bool {
	role := p.orgRole(orgID)
	return role == "owner" || role == "admin"
}

func (s *Server) withPrincipal(h func(http.ResponseWriter, *http.Request, *principal), allowedDuringSetup bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.HasPrefix(token, apiKeyPrefix) {
			if p, ok := s.apiKeyPrincipal(w, r, token); ok {
				h(w, r, p)
			}
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		hash := secrets.SHA256(c.Value)
		p := &principal{sessionHash: hash, roles: map[string]string{}, compliance: map[string]bool{}}
		err = s.db.QueryRow(r.Context(), `
			UPDATE sessions s SET last_seen_at = now()
			FROM users u
			WHERE s.id_hash = $1 AND s.expires_at > now() AND u.id = s.user_id AND u.status = 'active'
			RETURNING u.id, u.email, u.display_name, u.is_platform_admin, u.status, s.csrf_token, s.provider_id, s.id_token, s.provider_id IS NULL AND `+twoFactorSetupPending, hash).
			Scan(&p.User.ID, &p.User.Email, &p.User.DisplayName, &p.User.IsPlatformAdmin, &p.User.Status, &p.csrf, &p.providerID, &p.idToken, &p.twoFactorSetup)
		if errors.Is(err, pgx.ErrNoRows) {
			clearCookie(w, sessionCookie, s.cfg.SecureCookies())
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Your session has ended. Sign in again.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !constantTimeEqual(r.Header.Get("X-CSRF-Token"), p.csrf) {
				writeError(w, http.StatusForbidden, "csrf", "The request is missing a valid CSRF token. Reload the page and try again.")
				return
			}
		}
		if p.twoFactorSetup && !allowedDuringSetup {
			writeError(w, http.StatusForbidden, "two_factor_setup_required", "Your organization requires two-factor authentication. Set it up under Settings, Security to continue.")
			return
		}
		rows, err := s.db.Query(r.Context(), `SELECT org_id, role, compliance FROM organization_members WHERE user_id = $1`, p.User.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for rows.Next() {
			var org, role string
			var compliance bool
			if err := rows.Scan(&org, &role, &compliance); err != nil {
				rows.Close()
				s.internalError(w, r, err)
				return
			}
			p.roles[org] = role
			p.compliance[org] = compliance
		}
		rows.Close()
		h(w, r, p)
	}
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, message
	writeJSON(w, status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong on the server. Try again, and check the server logs if it keeps happening.")
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "The requested resource does not exist or you do not have access to it.")
}

func forbidden(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "forbidden", "You do not have permission to do this.")
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The request body is too large.")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "The request body is not valid JSON for this endpoint: "+err.Error())
		}
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "The request body must contain a single JSON object.")
		return false
	}
	return true
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func pathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := strings.ToLower(r.PathValue(name))
	if !uuidPattern.MatchString(id) {
		notFound(w)
		return "", false
	}
	return id, true
}

func (s *Server) clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && s.cfg.TrustProxy {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(ctx context.Context, r *http.Request, p *principal, orgID *string, action, targetType, targetID string, meta map[string]any) {
	var actor *string
	if meta == nil {
		meta = map[string]any{}
	}
	switch {
	case p != nil && p.apiKey != nil:
		meta["apiKeyId"], meta["apiKeyName"] = p.apiKey.ID, p.apiKey.Name
	case p != nil:
		actor = &p.User.ID
	}
	var ip string
	if r != nil {
		ip = s.clientIP(r)
	}
	var eventID int64
	var at time.Time
	err := s.db.QueryRow(ctx, `INSERT INTO audit_events (actor_user_id, org_id, action, target_type, target_id, ip, metadata) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
		actor, orgID, action, targetType, targetID, ip, meta).Scan(&eventID, &at)
	if err != nil {
		s.log.Error("audit write failed", "action", action, "err", err)
		return
	}
	if orgID != nil && !strings.HasPrefix(action, "compliance.") {
		s.enqueueWebhooks(ctx, *orgID, action, map[string]any{
			"id": strconv.FormatInt(eventID, 10), "type": action, "createdAt": at, "organizationId": *orgID,
			"actor":  map[string]any{"userId": actor, "apiKeyId": meta["apiKeyId"]},
			"target": map[string]string{"type": targetType, "id": targetID},
			"data":   meta,
		})
	}
}

func (s *Server) allow(ctx context.Context, key string, perMinute int64) bool {
	window := time.Now().Unix() / 60
	k := "aether:ratelimit:" + key + ":" + strconv.FormatInt(window, 10)
	pipe := s.kv.TxPipeline()
	count := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		s.log.Error("rate limit check failed; denying request", "err", err)
		return false
	}
	return count.Val() <= perMinute
}
