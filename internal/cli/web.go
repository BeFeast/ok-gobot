package cli

import (
	"fmt"
	"log"
	"net/http"

	"github.com/spf13/cobra"

	"ok-gobot/web"
)

func newWebCommand() *cobra.Command {
	var (
		addr      string
		noBrowser bool
	)

	cmd := &cobra.Command{
		Use:   "web",
		Short: "Launch the web UI",
		Long:  "Start an HTTP server serving the ok-gobot web UI and open it in the default browser.",
		RunE: func(cmd *cobra.Command, args []string) error {
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(web.IndexHTML)
			})

			url := fmt.Sprintf("http://%s", addr)
			log.Printf("[web] starting server on %s", url)

			if !noBrowser {
				go openBrowser(url)
			}

			return http.ListenAndServe(addr, mux)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8788", "HTTP listen address")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open browser automatically")

	return cmd
}
