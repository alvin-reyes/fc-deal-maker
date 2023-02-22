package config

import (
	"github.com/caarlos0/env/v6"
	logging "github.com/ipfs/go-log/v2"
	"github.com/joho/godotenv"
)

var (
	log = logging.Logger("config")
)

type DeltaConfig struct {
	Node struct {
		Name        string `env:"NODE_NAME" envDefault:"stg-deal-maker"`
		Description string `env:"NODE_DESCRIPTION"`
		Type        string `env:"NODE_TYPE"`
	}

	Dispatcher struct {
		DispatchJobsEvery      int `env:"DISPATCH_JOBS_EVERY" envDefault:"10"`
		MaxDispatchWorkers     int `env:"MAX_DISPATCH_WORKERS" envDefault:"5000"`
		MaxCleanupWorkers      int `env:"MAX_CLEANUP_WORKERS" envDefault:"1500"`
		MinerInfoUpdateJobFreq int `env:"MINER_INFO_UPDATE_JOB_FREQ" envDefault:"300"`
		CleanUpJobFreq         int `env:"CLEAN_UP_JOB_FREQ" envDefault:"1"`
	}

	Common struct {
		Mode  string `env:"MODE" envDefault:"standalone"`
		DBDSN string `env:"DB_DSN" envDefault:"stg-deal-maker"`
	}
}

func InitConfig() DeltaConfig {
	godotenv.Load() // load from environment OR .env file if it exists
	var cfg DeltaConfig

	if err := env.Parse(&cfg); err != nil {
		log.Fatal("error parsing config: %+v\n", err)
	}

	log.Debug("config parsed successfully")

	return cfg
}