package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apex/log"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/scp"
	"github.com/gliderlabs/ssh"
	mcdb "github.com/materials-commons/gomcdb"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"github.com/materials-commons/mc-ssh/pkg/mcscp"
	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "mc-sftp",
	Short: "A brief description of your application",
	Long: `A longer description that spans multiple lines and likely contains
examples and usage of using your application. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: mcsshdMain,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.mc-sftp.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

const host = "localhost"
const port = 23234
const root = "/tmp/scp/testdata"

var userStore *store.UserStore

func passwordHandler(context ssh.Context, password string) bool {
	userSlug := context.User()
	user, err := userStore.GetUserBySlug(userSlug)
	if err != nil {
		log.Errorf("Invalid user slug %q: %s", userSlug, err)
		return false
	}

	if err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		log.Errorf("Invalid password")
		return false
	}

	context.SetValue("mcuser", user)

	return true
}

func withMiddleware(middlewareList ...wish.Middleware) ssh.Handler {
	handler := func(session ssh.Session) {}
	for _, middleware := range middlewareList {
		handler = middleware(handler)
	}
	return handler
}

func proxyMiddleware() wish.Middleware {
	return func(handler ssh.Handler) ssh.Handler {
		return func(session ssh.Session) {
			cmd := session.Command()
			if len(cmd) == 0 {
				fmt.Println("no command")
				return
			}

			fmt.Println("cmd = ", cmd[0])
			if cmd[0] == "scp" {
				//filesystemHandler := scp.NewFileSystemHandler(root)
				//filesystemHandler(session)
				return
			}
		}
	}
}

func sftpMiddleware() wish.Middleware {
	return func(handler ssh.Handler) ssh.Handler {
		fmt.Println("handler for sftpMiddleware")
		return func(session ssh.Session) {
			fmt.Println("Starting NewRequestServer")
			user := session.Context().Value("mcuser").(*mcmodel.User)
			fmt.Printf("%+v\n", user)
			if true {
				return
			}
			channel := session
			root := sftp.InMemHandler()
			server := sftp.NewRequestServer(channel, root)
			if err := server.Serve(); err == io.EOF {
				server.Close()
			} else if err != nil {
				log.Fatalf("sftp server completed with error:", err)
			}
			handler(session)
		}
	}
}

func mcsshdMain(cmd *cobra.Command, args []string) {
	db := mcdb.MustConnectToDB()
	userStore = store.NewUserStore(db)
	handler := mcscp.NewMCFSHandler(db, root)
	//handler := scp.NewFileSystemHandler(root)
	fmt.Println("SCP Root:", root)
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithPasswordAuth(passwordHandler),
		//wish.WithHostKeyPath(".ssh/term_info_ed25519"),
		wish.WithMiddleware(
			//proxyMiddleware(),
			scp.Middleware(handler, handler),
			//sftpMiddleware(),
		),
	)
	s.SubsystemHandlers = make(map[string]ssh.SubsystemHandler)
	s.SubsystemHandlers["sftp"] = func(s ssh.Session) {
		fmt.Println("sftp")
		user := s.Context().Value("mcuser").(*mcmodel.User)
		fmt.Printf("sftp Write: %+v\n", user)
		if true {
			return
		}
		channel := s
		root := sftp.InMemHandler()
		server := sftp.NewRequestServer(channel, root)
		if err := server.Serve(); err == io.EOF {
			server.Close()
		} else if err != nil {
			log.Errorf("sftp server completed with error: %s", err)
		}
	}
	if err != nil {
		log.Fatalf("%s", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Infof("Starting SSH server on %s:%d", host, port)
	go func() {
		if err = s.ListenAndServe(); err != nil {
			log.Fatalf("%s", err)
		}
	}()

	<-done
	log.Info("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalf("%s", err)
	}
}
