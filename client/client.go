package chclient

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	chshare "github.com/VHSgunzo/chisel/share"
	"github.com/VHSgunzo/chisel/share/ccrypto"
	"github.com/VHSgunzo/chisel/share/cio"
	"github.com/VHSgunzo/chisel/share/cnet"
	"github.com/VHSgunzo/chisel/share/settings"
	"github.com/VHSgunzo/chisel/share/tunnel"
	"github.com/gorilla/websocket"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
)

// Config represents a client configuration
type Config struct {
	Fingerprint      string
	Auth             string
	KeepAlive        time.Duration
	MaxRetryCount    int
	MaxRetryInterval time.Duration
	Server           string
	Usock            string
	Usockl           net.Listener
	Proxy            string
	Remotes          []string
	Headers          http.Header
	DialContext      func(ctx context.Context, network, addr string) (net.Conn, error)
	Verbose          bool
}

// Client represents a client instance
type Client struct {
	*cio.Logger
	config    *Config
	computed  settings.Config
	sshConfig *ssh.ClientConfig
	proxyURL  *url.URL
	server    string
	usock     string
	usockl    net.Listener
	connCount cnet.ConnCount
	stop      func()
	eg        *errgroup.Group
	tunnel    *tunnel.Tunnel
}

// NewClient creates a new client instance
func NewClient(c *Config) (*Client, error) {
	//apply default scheme
	if !strings.HasPrefix(c.Server, "http") &&
		!strings.HasPrefix(c.Server, "ws") {
		c.Server = "http://" + c.Server
	}
	if c.MaxRetryInterval < time.Second {
		c.MaxRetryInterval = 5 * time.Minute
	}
	u, err := url.Parse(c.Server)
	if err != nil {
		return nil, err
	}
	//swap to websockets scheme
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	//apply default port
	if !regexp.MustCompile(`:\d+$`).MatchString(u.Host) {
		u.Host = u.Host + ":2871"
	}
	hasReverse := false
	hasSocks := false
	hasStdio := false
	client := &Client{
		Logger: cio.NewLogger("client"),
		config: c,
		computed: settings.Config{
			Version: chshare.BuildVersion,
		},
		server: u.String(),
		usock:  c.Usock,
		usockl: c.Usockl,
	}
	//set default log level
	client.Logger.Info = true
	client.Logger.Debug = c.Verbose
	//validate remotes
	for _, s := range c.Remotes {
		r, err := settings.DecodeRemote(s)
		if err != nil {
			return nil, fmt.Errorf("Failed to decode remote '%s': %s", s, err)
		}
		if r.Socks {
			hasSocks = true
		}
		if r.Reverse {
			hasReverse = true
		}
		if r.Stdio {
			if hasStdio {
				return nil, errors.New("Only one stdio is allowed")
			}
			hasStdio = true
		}
		//confirm non-reverse tunnel is available
		if !r.Reverse && !r.Stdio && !r.CanListen() {
			return nil, fmt.Errorf("Client cannot listen on %s", r.String())
		}
		client.computed.Remotes = append(client.computed.Remotes, r)
	}
	//outbound proxy
	if p := c.Proxy; p != "" {
		client.proxyURL, err = url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("Invalid proxy URL (%s)", err)
		}
	}
	//ssh auth and config
	user, pass := settings.ParseAuth(c.Auth)
	client.sshConfig = &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		ClientVersion:   "SSH-" + chshare.ProtocolVersion + "-client",
		HostKeyCallback: client.verifyServer,
		Timeout:         settings.EnvDuration("SSH_TIMEOUT", 30*time.Second),
	}
	//prepare client tunnel
	client.tunnel = tunnel.New(tunnel.Config{
		Logger:    client.Logger,
		Inbound:   true, //client always accepts inbound
		Outbound:  hasReverse,
		Socks:     hasReverse && hasSocks,
		KeepAlive: client.config.KeepAlive,
	})
	return client, nil
}

// Run starts client and blocks while connected
func (c *Client) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		return err
	}
	return c.Wait()
}

func (c *Client) verifyServer(hostname string, remote net.Addr, key ssh.PublicKey) error {
	expect := c.config.Fingerprint
	if expect == "" {
		return nil
	}
	got := ccrypto.FingerprintKey(key)
	_, err := base64.StdEncoding.DecodeString(expect)
	if _, ok := err.(base64.CorruptInputError); ok {
		c.Logger.Infof("Specified deprecated MD5 fingerprint (%s), please update to the new SHA256 fingerprint: %s", expect, got)
		return c.verifyLegacyFingerprint(key)
	} else if err != nil {
		return fmt.Errorf("Error decoding fingerprint: %w", err)
	}
	if got != expect {
		return fmt.Errorf("Invalid fingerprint (%s)", got)
	}
	//overwrite with complete fingerprint
	c.Infof("Fingerprint %s", got)
	return nil
}

// verifyLegacyFingerprint calculates and compares legacy MD5 fingerprints
func (c *Client) verifyLegacyFingerprint(key ssh.PublicKey) error {
	bytes := md5.Sum(key.Marshal())
	strbytes := make([]string, len(bytes))
	for i, b := range bytes {
		strbytes[i] = fmt.Sprintf("%02x", b)
	}
	got := strings.Join(strbytes, ":")
	expect := c.config.Fingerprint
	if !strings.HasPrefix(got, expect) {
		return fmt.Errorf("Invalid fingerprint (%s)", got)
	}
	return nil
}

// Start client and does not block
func (c *Client) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.stop = cancel
	eg, ctx := errgroup.WithContext(ctx)
	c.eg = eg
	via := ""
	if c.proxyURL != nil {
		via = " via " + c.proxyURL.String()
	}
	if c.usock != "" {
		c.Infof("Connecting to unix:%s\n", c.usock)
	} else {
		c.Infof("Connecting to %s%s\n", c.server, via)
	}
	//connect to chisel server
	eg.Go(func() error {
		return c.connectionLoop(ctx)
	})
	//listen sockets
	eg.Go(func() error {
		clientInbound := c.computed.Remotes.Reversed(false)
		if len(clientInbound) == 0 {
			return nil
		}
		return c.tunnel.BindRemotes(ctx, clientInbound)
	})
	return nil
}

func (c *Client) setProxy(u *url.URL, d *websocket.Dialer) error {
	// CONNECT proxy
	if !strings.HasPrefix(u.Scheme, "socks") {
		d.Proxy = func(*http.Request) (*url.URL, error) {
			return u, nil
		}
		return nil
	}
	// SOCKS5 proxy
	if u.Scheme != "socks" && u.Scheme != "socks5h" {
		return fmt.Errorf(
			"unsupported socks proxy type: %s:// (only socks5h:// or socks:// is supported)",
			u.Scheme,
		)
	}
	var auth *proxy.Auth
	if u.User != nil {
		pass, _ := u.User.Password()
		auth = &proxy.Auth{
			User:     u.User.Username(),
			Password: pass,
		}
	}
	socksDialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
	if err != nil {
		return err
	}
	d.NetDial = socksDialer.Dial
	return nil
}

// Wait blocks while the client is running.
func (c *Client) Wait() error {
	return c.eg.Wait()
}

// Close manually stops the client
func (c *Client) Close() error {
	if c.stop != nil {
		c.stop()
	}
	return nil
}
