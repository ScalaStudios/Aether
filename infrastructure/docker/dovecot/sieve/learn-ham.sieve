require ["vnd.dovecot.pipe", "copy", "imapsieve", "environment", "variables"];
if environment :is "imap.mailbox" "Trash" {
  stop;
}
if environment :matches "imap.user" "*" {
  pipe :copy "aether-learn" ["ham", "${1}"];
}
