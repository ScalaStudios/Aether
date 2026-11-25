# Encryption

Aether Mail can sign and encrypt with S/MIME or OpenPGP, and it can send a password-protected message to someone who does not have a key. The private key and the password stay in the browser. The server stores a wrapped private key it cannot open. For a password-protected message it stores only ciphertext.

Desktop mail apps do not receive the wrapped key. After unlocking a key in **Settings → Encryption**, export it and import that file into the other app. Mail sealed in the browser is ordinary S/MIME or OpenPGP on the wire, so an app that has the key can open it.

## Organization policy

Owners and administrators set three choices under **Administration → Encryption**: S/MIME, OpenPGP, and password-protected mail. Each is Off, Allow, or Require.

- **Off** hides that method, and the server rejects it.
- **Allow** leaves the choice to the sender.
- **Require** means outgoing mail must be encrypted with a method that is required. A signature on its own does not count. When more than one method is required, any one of them is enough.
- A password requirement applies to a recipient whose domain is not one of the organization's verified domains. Encrypting that message to their S/MIME certificate or OpenPGP key satisfies it. People on verified domains can still receive ordinary mail when only password-protected mail is required.
- A password-protected message does not satisfy a requirement for S/MIME or OpenPGP.

The policy is checked when the message is sent, and again when a scheduled S/MIME or OpenPGP message comes due. Password-protected messages cannot be scheduled.

## S/MIME

Import a PKCS#12 file, or a certificate and an RSA private key in PEM, under **Settings → Encryption**. The certificate has to include the mailbox address. Aether signs and decrypts with RSA. The fingerprint is the SHA-256 hash of the certificate.

The browser wraps the private key with AES-256-GCM before the upload. The wrapping key is PBKDF2-HMAC-SHA256 of a passphrase you choose, with 210,000 iterations and a random salt. The stored bytes start with `0xA1`. The passphrase is not sent, and there is no server request that unlocks a key. Unlocking keeps the key in this browser until you lock it or sign out. A different account on the same browser locks the previous keys.

Outgoing mail is signed, or signed and then encrypted, in the browser. Incoming mail is decrypted and verified from the raw message, also in the browser. Recipient certificates come from keys people in your organizations have saved for that address, and from certificates you save out of mail you have verified.

## OpenPGP

Create a key in Settings, or paste a private key you already have. A key created here is version 4: an Ed25519 signing key and an encryption subkey. The same passphrase wrapping protects it. The server stores the armored public key and checks that it belongs to the mailbox address. A secret key in the public field is rejected.

Encrypting also signs. A message that is only signed uses a detached signature. The composer can add an Autocrypt header for your address. When a message arrives with one, you can save that public key. If a recipient has no saved key, Aether looks one up in their Web Key Directory. It tries the advanced host first, `https://openpgpkey.example.com/.well-known/openpgpkey/example.com/hu/…`, and uses the direct URL only when that host name does not exist. The lookup runs on the server, does not follow redirects, and refuses addresses that point at a private network.

## Password-protected messages

Choose **Password** in the composer. Aether encrypts the subject and the body in the browser with AES-256-GCM. The key is PBKDF2-HMAC-SHA256 of the password, 210,000 iterations, and the salt is stored beside the ciphertext. The password is not uploaded.

The recipient gets an email whose subject is "Password-protected message". The link's access token is in the URL fragment, so it is not sent to the server with the page request. They open the page, type the password you shared some other way, and can reply once. The reply is encrypted with the same password. The link expires after 1, 3, or 7 days. Only the owner of a mailbox can send one, to a single recipient, without attachments. It is not saved as a draft.

The sender's own link does not contain the access token. Opening it asks for the same password, and shows a reply when one arrives.

## What never reaches the server

- A private key in the clear, or a passphrase.
- The password of a protected message, or its subject and body in the clear.
- The access token as a query parameter. The server stores a SHA-256 hash of it. The proof that allows the one reply is inside the ciphertext, and the server stores only that proof's hash.
