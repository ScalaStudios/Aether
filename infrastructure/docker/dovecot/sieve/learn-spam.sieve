require ["vnd.dovecot.pipe", "copy", "imapsieve", "environment", "variables"];
if environment :matches "imap.user" "*" {
  pipe :copy "aether-learn" ["spam", "${1}"];
}
