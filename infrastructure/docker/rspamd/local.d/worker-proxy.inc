bind_socket = "*:11332";
milter = yes;
timeout = 120s;
upstream "local" {
  default = yes;
  self_scan = yes;
}
allow_file_and_shm_inputs = false;
