dns {
  nameserver = ["unbound:53"];
  timeout = 2s;
  retransmits = 3;
}
