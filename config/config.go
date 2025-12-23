package config

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var ValueOf = &config{}

type allowedUsers []int64

func (au *allowedUsers) Decode(value string) error {
	if value == "" {
		return nil
	}
	ids := strings.Split(value, ",")
	for _, id := range ids {
		idInt, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		*au = append(*au, idInt)
	}
	return nil
}

type config struct {
	APIID           int64    `envconfig:"API_ID" required:"true"`
	APIHash         string   `envconfig:"API_HASH" required:"true"`
	BotToken        string   `envconfig:"BOT_TOKEN" required:"true"`
	LogChannelID    int64    `envconfig:"LOG_CHANNEL" required:"true"`
	Host            string   `envconfig:"HOST"`
	Port            int      `envconfig:"PORT" default:"8080"`
	AllowedUsers    []int64  `envconfig:"ALLOWED_USERS"`
	ForceSubChannel string   `envconfig:"FORCE_SUB_CHANNEL"`
	// Added WorkerURL to map your Cloudflare Worker domain
	WorkerURL       string   `envconfig:"WORKER_URL" required:"true"`
	Dev             bool     `envconfig:"DEV" default:"false"`
	HashLength      int      `envconfig:"HASH_LENGTH" default:"6"`
	UseSessionFile  bool     `envconfig:"USE_SESSION_FILE" default:"true"`
	UserSession     string   `envconfig:"USER_SESSION"`
	UsePublicIP     bool     `envconfig:"USE_PUBLIC_IP" default:"false"`
	MultiTokens     []string
}

var botTokenRegex = regexp.MustCompile(`MULTI\_TOKEN\d+=(.*)`)

func (c *config) loadFromEnvFile(log *zap.Logger) {
	envPath := filepath.Clean("fsb.env")
	err := godotenv.Load(envPath)
	if err != nil && !os.IsNotExist(err) {
		log.Fatal("Error parsing env file", zap.Error(err))
	}
}

func (c *config) SetFlagsFromConfig(cmd *cobra.Command) {
	cmd.Flags().Int64Var(&c.APIID, "api-id", 0, "Telegram API ID")
	cmd.Flags().StringVar(&c.APIHash, "api-hash", "", "Telegram API Hash")
	cmd.Flags().StringVar(&c.BotToken, "bot-token", "", "Telegram Bot Token")
	cmd.Flags().Int64Var(&c.LogChannelID, "log-channel", 0, "Log Channel ID")
	cmd.Flags().StringVar(&c.Host, "host", "", "Host URL")
	cmd.Flags().IntVar(&c.Port, "port", 0, "Port")
	cmd.Flags().StringVar(&c.ForceSubChannel, "force-sub-channel", "", "Force Sub Channel")
	// Added flag for Worker URL
	cmd.Flags().StringVar(&c.WorkerURL, "worker-url", "", "Cloudflare Worker URL")
}

func (c *config) loadConfigFromArgs(log *zap.Logger, cmd *cobra.Command) {
	if c.APIID != 0 {
		os.Setenv("API_ID", strconv.FormatInt(c.APIID, 10))
	}
	if c.APIHash != "" {
		os.Setenv("API_HASH", c.APIHash)
	}
	if c.BotToken != "" {
		os.Setenv("BOT_TOKEN", c.BotToken)
	}
	if c.LogChannelID != 0 {
		os.Setenv("LOG_CHANNEL", strconv.FormatInt(c.LogChannelID, 10))
	}
	if c.WorkerURL != "" {
		os.Setenv("WORKER_URL", c.WorkerURL)
	}
	// ... (Rest of the manual mappings if necessary)
}

func (c *config) setupEnvVars(log *zap.Logger, cmd *cobra.Command) {
	c.loadFromEnvFile(log)
	c.loadConfigFromArgs(log, cmd)
	
	err := envconfig.Process("", c)
	if err != nil {
		log.Fatal("Error processing env vars", zap.Error(err))
	}

	ip, _ := getIP(c.UsePublicIP)
	if c.Host == "" {
		c.Host = "http://" + ip + ":" + strconv.Itoa(c.Port)
	}

	// Dynamic multi-token detection
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "MULTI_TOKEN") {
			match := botTokenRegex.FindStringSubmatch(env)
			if len(match) > 1 {
				c.MultiTokens = append(c.MultiTokens, match[1])
			}
		}
	}
}

func Load(log *zap.Logger, cmd *cobra.Command) {
	log = log.Named("Config")
	ValueOf.setupEnvVars(log, cmd)
	
	// Normalize LogChannelID
	ValueOf.LogChannelID = int64(stripInt(log, int(ValueOf.LogChannelID)))
	
	if ValueOf.HashLength < 5 || ValueOf.HashLength > 32 {
		log.Info("Normalizing HASH_LENGTH to 6")
		ValueOf.HashLength = 6
	}
	log.Info("Config loaded successfully")
}

// Internal IP and Public IP helpers (Keep your existing implementations here)
func getIP(public bool) (string, error) {
	if public { return GetPublicIP() }
	return getInternalIP()
}

func getInternalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil { return "localhost", err }
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func GetPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org?format=text")
	if err != nil { return "localhost", err }
	defer resp.Body.Close()
	ip, _ := io.ReadAll(resp.Body)
	return string(ip), nil
}

func stripInt(log *zap.Logger, a int) int {
	strA := strconv.Itoa(abs(a))
	lastDigits := strings.Replace(strA, "100", "", 1)
	result, _ := strconv.Atoi(lastDigits)
	return result
}

func abs(x int) int {
	if x < 0 { return -x }
	return x
}
