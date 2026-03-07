package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/web"
)

func newWebCommand(cfg *config.Config) *cobra.Command {
	var (
		addr      string
		noBrowser bool
	)

	cmd := &cobra.Command{
		Use:   "web",
		Short: "Launch the web UI",
		Long:  "Start an HTTP server serving the ok-gobot web UI and open it in the default browser.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Inject the control server port so the JS connects to the
			// correct WebSocket regardless of which port serves the web UI.
			controlPort := cfg.Control.Port
			if controlPort == 0 {
				controlPort = 8787
			}
			html := strings.Replace(string(web.IndexHTML),
				"</head>",
				fmt.Sprintf("<script>window.CONTROL_PORT=%d;</script></head>", controlPort),
				1)

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write([]byte(html))
			})
			mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				models := cfg.Models
				if models == nil {
					models = []string{}
				}
				json.NewEncoder(w).Encode(models)
			})

			srv := &http.Server{Addr: addr, Handler: mux}

			// Graceful shutdown on SIGINT/SIGTERM.
			sigCh := make(chan os.Signal, 1)
			quit := make(chan struct{})
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			go func() {
				select {
				case <-sigCh:
					log.Println("[web] shutting down...")
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					srv.Shutdown(ctx) //nolint:errcheck
				case <-quit:
				}
				signal.Stop(sigCh)
			}()

			url := fmt.Sprintf("http://%s", addr)
			log.Printf("[web] starting server on %s", url)

			if !noBrowser {
				go openBrowser(url)
			}

			err := srv.ListenAndServe()
			close(quit) // unblock signal goroutine if server exited on its own
			if err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8788", "HTTP listen address")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open browser automatically")

	return cmd
}
