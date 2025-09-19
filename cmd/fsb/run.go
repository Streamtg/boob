package main

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/commands" // Añadido para cargar LoadStream
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/routes"
	"EverythingSuckz/fsb/internal/types"
	"EverythingSuckz/fsb/internal/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/spf13/cobra"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const versionString = "3.1.0"

var rootCmd = &cobra.Command{
	Use:               "fsb [command]",
	Short:             "Telegram File Stream Bot",
	Long:              "Telegram Bot to generate direct streamable links for telegram media.",
	Example:           "fsb run --port 8080",
	Version:           versionString,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var startTime time.Time = time.Now()

var runCmd = &cobra.Command{
	Use:                "run",
	Short:              "Run the bot with the given configuration.",
	DisableSuggestions: false,
	Run:                runApp,
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Generate a session string for the user account",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Generando sesión... (implementación no modificada)")
	},
}

func runApp(cmd *cobra.Command, args []string) {
	utils.InitLogger(config.ValueOf.Dev)
	log := utils.Logger
	mainLogger := log.Named("Main")
	mainLogger.Info("Starting server")
	config.Load(log, cmd)
	router := getRouter(log)

	mainBot, err := bot.StartClient(log)
	if err != nil {
		log.Panic("Failed to start main bot", zap.Error(err))
	}

	// Inicializar database
	err = database.InitDatabase(log)
	if err != nil {
		log.Panic("Failed to initialize database", zap.Error(err))
	}

	cache.InitCache(log)
	cache.InitStatsCache(log)
	workers, err := bot.StartWorkers(log)
	if err != nil {
		log.Panic("Failed to start workers", zap.Error(err))
		return
	}
	workers.AddDefaultClient(mainBot, mainBot.Self)

	// Cargar el handler de streaming
	cmdInstance := &commands.Command{
		Log: log,
	}
	cmdInstance.LoadStream(mainBot.Dispatcher)

	bot.StartUserBot(log)
	mainLogger.Info("Server started", zap.Int("port", config.ValueOf.Port))
	mainLogger.Info("File Stream Bot", zap.String("version", versionString))
	mainLogger.Sugar().Infof("Server is running at %s", config.ValueOf.Host)
	err = router.Run(fmt.Sprintf(":%d", config.ValueOf.Port))
	if err != nil {
		mainLogger.Sugar().Fatalln(err)
	}
}

func getRouter(log *zap.Logger) *gin.Engine {
	if config.ValueOf.Dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.Default()
	router.Use(gin.ErrorLogger())
	router.GET("/", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, types.RootResponse{
			Message: "Server is running.",
			Ok:      true,
			Uptime:  utils.TimeFormat(uint64(time.Since(startTime).Seconds())),
			Version: versionString,
		})
	})
	routes.Load(log, router)
	return router
}

func init() {
	config.ValueOf.SetFlagsFromConfig(runCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.SetVersionTemplate(fmt.Sprintf(`Telegram File Stream Bot version %s`, versionString))
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
