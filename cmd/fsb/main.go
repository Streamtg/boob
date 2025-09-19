package main

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/commands"
	"fmt"
	"os"

	"github.com/celestix/gotgproto"
	"github.com/spf13/cobra"
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

// runCmd inicializa y ejecuta el bot de Telegram
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the Telegram File Stream Bot",
	Run: func(cmd *cobra.Command, args []string) {
		// Inicializar logger
		logger, err := zap.NewProduction()
		if err != nil {
			fmt.Printf("Error iniciando logger: %v\n", err)
			os.Exit(1)
		}
		defer logger.Sync()

		// Cargar configuración desde fsb.env
		if err := config.ValueOf.LoadConfig(); err != nil {
			logger.Fatal("Error cargando configuración", zap.Error(err))
			os.Exit(1)
		}

		// Configurar cliente de Telegram
		client, err := gotgproto.NewClient(
			config.ValueOf.APIID,
			config.ValueOf.APIHash,
			config.ValueOf.BotToken,
			&gotgproto.ClientOpts{},
		)
		if err != nil {
			logger.Fatal("Error iniciando cliente de Telegram", zap.Error(err))
			os.Exit(1)
		}

		// Crear instancia de comandos con el logger
		cmdInstance := &commands.Command{
			Log: logger,
		}

		// Cargar el handler de streaming
		dispatcher := client.Dispatcher
		cmdInstance.LoadStream(dispatcher)

		// Iniciar el cliente
		logger.Info("Bot iniciado correctamente")
		if err := client.Run(); err != nil {
			logger.Fatal("Error ejecutando el bot", zap.Error(err))
			os.Exit(1)
		}
	},
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Generate a session string for the user account",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Generando sesión... (implementación no modificada)")
	},
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
