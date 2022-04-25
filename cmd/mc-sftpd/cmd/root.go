/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

*/
package cmd

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"

	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "mc-sftpd",
	Short: "A brief description of your application",
	Long: `A longer description that spans multiple lines and likely contains
examples and usage of using your application. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,

	Run: mcsftpdMain,
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

func mcsftpdMain(cmd *cobra.Command, args []string) {

	var (
		readOnly    bool
		debugStderr bool
	)

	flag.BoolVar(&readOnly, "R", false, "read-only server")
	flag.BoolVar(&debugStderr, "e", false, "debug to stderr")
	flag.Parse()

	debugStream := ioutil.Discard
	if debugStderr {
		debugStream = os.Stderr
	}

	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			// Should use constant-time compare (or better, salt+hash) in
			// a production setting.
			fmt.Fprintf(debugStream, "Login: %s\n", c.User())
			if c.User() == "testuser" && string(pass) == "tiger" {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected for %q", c.User())
		},
	}

	//privateBytes, err := ioutil.ReadFile("id_rsa")
	//if err != nil {
	//	log.Fatal("Failed to load private key", err)
	//}
	//
	//private, err := ssh.ParsePrivateKey(privateBytes)
	//if err != nil {
	//	log.Fatal("Failed to parse private key", err)
	//}
	//
	//config.AddHostKey(private)

	listener, err := net.Listen("tcp", "0.0.0.0:2022")
	if err != nil {
		log.Fatal("failed to listen for connection", err)
	}
	fmt.Printf("Listening on %v\n", listener.Addr())

	for {
		nConn, err := listener.Accept()
		if err != nil {
			log.Fatal("failed to accept incoming connection", err)
		}

		go handleConnection(nConn, config)
	}
}

func handleConnection(conn net.Conn, config *ssh.ServerConfig) {
	// Before use, a handshake must be performed on the incoming net.Conn.
	sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		log.Fatal("failed to handshake", err)
	}
	log.Println("login detected:", sconn.User())
	//fmt.Fprintf(debugStream, "SSH server established\n")

	// The incoming Request channel must be serviced.
	go ssh.DiscardRequests(reqs)

	// Service the incoming Channel channel.
	for newChannel := range chans {
		// Channels have a type, depending on the application level
		// protocol intended. In the case of an SFTP session, this is "subsystem"
		// with a payload string of "<length=4>sftp"
		//fmt.Fprintf(debugStream, "Incoming channel: %s\n", newChannel.ChannelType())
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			//fmt.Fprintf(debugStream, "Unknown channel type: %s\n", newChannel.ChannelType())
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Fatal("could not accept channel.", err)
		}
		//fmt.Fprintf(debugStream, "Channel accepted\n")

		// Sessions have out-of-band requests such as "shell",
		// "pty-req" and "env".  Here we handle only the
		// "subsystem" request.
		go func(in <-chan *ssh.Request) {
			for req := range in {
				//fmt.Fprintf(debugStream, "Request: %v\n", req.Type)
				ok := false
				switch req.Type {
				case "subsystem":
					//fmt.Fprintf(debugStream, "Subsystem: %s\n", req.Payload[4:])
					if string(req.Payload[4:]) == "sftp" {
						ok = true
					}
				}
				//fmt.Fprintf(debugStream, " - accepted: %v\n", ok)
				req.Reply(ok, nil)
			}
		}(requests)

		root := sftp.InMemHandler()
		server := sftp.NewRequestServer(channel, root)
		if err := server.Serve(); err == io.EOF {
			server.Close()
			log.Print("sftp client exited session.")
		} else if err != nil {
			log.Fatal("sftp server completed with error:", err)
		}
	}
}
