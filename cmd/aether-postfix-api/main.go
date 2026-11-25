package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/scalastudios/aether/internal/mail/queue"
)

func main() {
	log.SetPrefix("aether-postfix-api: ")
	key := os.Getenv("AETHER_POSTFIX_API_KEY")
	if key == "" {
		log.Fatal("AETHER_POSTFIX_API_KEY is required")
	}
	agent := &queue.Agent{Key: key, Spool: "/var/spool/postfix", Postqueue: "postqueue", Postsuper: "postsuper"}
	srv := &http.Server{Addr: ":8025", Handler: agent.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Printf("listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}
