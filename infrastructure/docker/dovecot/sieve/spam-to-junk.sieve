require ["fileinto", "variables", "envelope", "relational", "comparator-i;ascii-numeric", "vnd.dovecot.execute"];
if header :is "X-Spam" "Yes" {
  set "score" "999";
  if header :matches "X-Spam-Status" "*score=*" {
    set "score" "${2}";
  }
  set "from" "";
  if address :all :matches "from" "*" {
    set "from" "${1}";
  }
  set "reply" "";
  if header :matches "In-Reply-To" "*" {
    set "reply" "${1}";
  }
  set "rcpt" "";
  if envelope :all :matches "to" "*" {
    set "rcpt" "${1}";
  }
  if anyof (string :value "ge" :comparator "i;ascii-numeric" "${score}" "10",
            header :contains "Authentication-Results" "dmarc=fail",
            not execute "aether-trusted" ["${rcpt}", "${from}", "${reply}"]) {
    fileinto "Junk";
    stop;
  }
}
