package cmd

import (
	"context"
	"errors"
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
	Use:   "mc-sshd",
	Short: "SSH Server for Materials Commons that handles SFTP and SCP requests.",
	Long: `mc-sshd is a custom SSH server that only implements the SFTP and SCP services. It connects
these services to Materials Commons.`,
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
	mcfsRoot = os.Getenv("MCFS_DIR")
	if mcfsRoot == "" {
		log.Fatalf("MCFS_DIR is unset or blank")
	}

	log.Infof("MCFS Root: %s", mcfsRoot)
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
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
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
