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
	"github.com/materials-commons/mc-ssh/pkg/mc"
	"github.com/materials-commons/mc-ssh/pkg/mcscp"
	"github.com/materials-commons/mc-ssh/pkg/mcsftp"
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
	mcfsRoot = os.Getenv("MCFS_DIR")
	if mcfsRoot == "" {
		log.Fatalf("MCFS_DIR is unset or blank")
	}
}

const host = "localhost"
const port = 23234

var mcfsRoot string
var stores *mc.Stores
var userStore store.UserStore

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

func mcsshdMain(cmd *cobra.Command, args []string) {
	db := mcdb.MustConnectToDB()
	stores = mc.NewGormStores(db, mcfsRoot)
	userStore = store.NewGormUserStore(db)
	s := mustSetupSSHServerAndServices()
	runServer(s)
}

func mustSetupSSHServerAndServices() *ssh.Server {
	s := mustCreateSSHServerWithSCPHandling(stores)
	setupSFTPSubsystem(s)
	return s
}

func mustCreateSSHServerWithSCPHandling(stores *mc.Stores) *ssh.Server {
	handler := mcscp.NewMCFSHandler(stores, mcfsRoot)
	fmt.Println("SCP Root:", mcfsRoot)
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithPasswordAuth(passwordHandler),
		//wish.WithHostKeyPath(".ssh/term_info_ed25519"),
		wish.WithMiddleware(scp.Middleware(handler, handler)),
	)

	if err != nil {
		log.Fatalf("Failed creating SSH Server: %s", err)
	}

	return s
}

func setupSFTPSubsystem(s *ssh.Server) {
	// SFTP is a subsystem, so rather than being handled as middleware we have to set
	// the subsystem handler.
	s.SubsystemHandlers = make(map[string]ssh.SubsystemHandler)
	s.SubsystemHandlers["sftp"] = func(s ssh.Session) {
		user := s.Context().Value("mcuser").(*mcmodel.User)
		//handler := sftp.InMemHandler()
		h := mcsftp.NewMCFSHandler(user, stores, mcfsRoot)
		server := sftp.NewRequestServer(s, h)
		if err := server.Serve(); err == io.EOF {
			_ = server.Close()
		} else if err != nil {
			log.Errorf("sftp server completed with error: %s", err)
		}
	}
}

func runServer(s *ssh.Server) {
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Infof("Starting SSH server on %s:%d", host, port)
	go func() {
		if err := s.ListenAndServe(); err != nil {
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
