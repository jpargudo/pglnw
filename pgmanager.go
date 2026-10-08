package main

import (
    "context"
    "fmt"
    "time"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgconn"
    "os"
    "encoding/json"
    "errors"
)

const (
  // Default maximum time pglnw keeps trying to reconnect when the connection
  // is lost. Can be overridden with "ReconnectTimeout" in the config.json
  defaultReconnectTimeout = 60 * time.Second

  // Maximum duration of ONE connection attempt (connect + ping). Without it,
  // an unreachable server (dropped packets, half-open TCP connection) would
  // block pgx.Connect() far beyond the reconnect timeout.
  pgAttemptTimeout = 5 * time.Second
)

// ConfigDuration is a time.Duration that can be read from the JSON config
// either as a string understood by time.ParseDuration ("90s", "2m", "1m30s")
// or as a number of seconds (90)
type ConfigDuration time.Duration

func (d *ConfigDuration) UnmarshalJSON(b []byte) error {
  var v interface{}
  if err := json.Unmarshal(b, &v); err != nil {
    return err
  }

  switch val := v.(type) {
  case float64:
    *d = ConfigDuration(time.Duration(val * float64(time.Second)))
  case string:
    parsed, err := time.ParseDuration(val)
    if err != nil {
      return fmt.Errorf("invalid duration %q (examples: \"90s\", \"2m\", 90): %w", val, err)
    }
    *d = ConfigDuration(parsed)
  default:
    return fmt.Errorf("invalid duration %s (examples: \"90s\", \"2m\", 90)", string(b))
  }
  return nil
}

// PGClientConfig represents the PostgreSQL client configuration
type PGClientConfig struct {
  Hostname         string          `json:"Hostname"`
  Port             string          `json:"Port"`
  Database         string          `json:"Database"`
  Username         string          `json:"Username"`
  Password         string          `json:"Password"`
  Sslmode          string          `json:"Sslmode"`
  ApplicationName  string          `json:"ApplicationName"`

  // Optional: maximum time to try to reconnect once the connection is lost.
  // Default is 60s when not set in the config.json
  ReconnectTimeout *ConfigDuration `json:"ReconnectTimeout"`
}


// PGManager represents the PostgreSQL connection manager
type PGManager struct {
	conn   *pgx.Conn
	Config *PGClientConfig

	// maximum time to try to reconnect (from ReconnectTimeout in config.json)
	ReconnectTimeout time.Duration
}

// NewPGManager creates a new PGManager instance with the given configuration
func NewPGManager(configPath string) (*PGManager, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}

	reconnectTimeout := defaultReconnectTimeout
	if config.ReconnectTimeout != nil {
		reconnectTimeout = time.Duration(*config.ReconnectTimeout)
		if reconnectTimeout <= 0 {
			return nil, fmt.Errorf("invalid \"ReconnectTimeout\" in %s: it must be greater than 0", configPath)
		}
	}

	return &PGManager{
		Config:           config,
		ReconnectTimeout: reconnectTimeout,
	}, nil
}

func loadConfig(configPath string) (*PGClientConfig, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	config := &PGClientConfig{}
	decoder := json.NewDecoder(file)
	err = decoder.Decode(config)
	if err != nil {
		return nil, err
	}

	return config, nil
}

// connString builds the connection string from the configuration
func (pm *PGManager) connString() string {
  return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=%s application_name=%s",
                     pm.Config.Hostname, pm.Config.Port, pm.Config.Database, pm.Config.Username,
                     pm.Config.Password, pm.Config.Sslmode, pm.Config.ApplicationName)
}

// PGConnect establishes a new connection to the PostgreSQL database
func (pm *PGManager) PGConnect() (*pgx.Conn, error) {

	conn, err := pgx.Connect(context.Background(), pm.connString())

	if err != nil {
		return nil, err
	}

	pm.conn = conn
	return conn, nil
}

// PGPing checks the current connection. It gives up after pgAttemptTimeout
// instead of blocking forever when the network silently drops the packets.
func (pm *PGManager) PGPing() error {
	ctx, cancel := context.WithTimeout(context.Background(), pgAttemptTimeout)
	defer cancel()
	return pm.conn.Ping(ctx)
}

// PGReconnectWithTimeout attempts to reconnect to the PostgreSQL database within a specified timeout
func (pm *PGManager) PGReconnectWithTimeout(timeout time.Duration, err error) error {

  var message string
	startTime := time.Now()
	lastErr := err

	for time.Since(startTime) < timeout {

    var pgErr *pgconn.PgError

    if errors.As(err, &pgErr) {
      switch (pgErr.Code) {
        case "25P01","25P02","25P03","25006":
          message = "PG server in recovery mode            "
        case "28000", "28P01":
          message = "Invalid auth                          "
        case "53300":
           message = "Max connection reached on the server "
        case "57P01":
           message = "PG terminated by admin cmd           "
        case "57P02":
           message = "PG crash shutdown                    "
        case "57P03":
           message = "Cannot connect now                   "
        case "57P04": 
           message = "Database dropped                     "
        case "57P05":
           message = "Idle session timeout                 "
        case "42601":
           message = "Syntax error in SQL                  "
        default:
           message = "Other error from PG                  "
      }

      fmt.Print(string(colorRed))
      if time.Since(startTime) < time.Second*1 {
        fmt.Printf("\r[%s] %s", pgErr.Code,message)
      } else {
        fmt.Printf("\r[%s] %s (downtime: %s)", pgErr.Code,message, time.Since(startTime).Round(time.Millisecond).String())
      }
      fmt.Print(string(colorReset))

    } else {
       fmt.Print(string(colorRed))
       if time.Since(startTime) < time.Millisecond*300 {
         fmt.Printf("\rReconnecting to PostgreSQL                          ")
       } else {
         fmt.Printf("\rReconnecting to PostgreSQL (downtime: %s)           ", time.Since(startTime).Round(time.Millisecond).String())
       }
       fmt.Print(string(colorReset))
    }

		// A single attempt can never last longer than pgAttemptTimeout, nor
		// than the time left before the global timeout is reached
		attemptTimeout := pgAttemptTimeout
		if remaining := timeout - time.Since(startTime); remaining < attemptTimeout {
			attemptTimeout = remaining
		}

		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		connErr := pm.pgConnectWithRetry(ctx)
		cancel()

		if connErr == nil {
      fmt.Print(string(colorGreen))
			fmt.Printf("\nReconnected successfully after %s downtime\n", time.Since(startTime).Round(time.Millisecond).String())
      fmt.Print(string(colorReset))
			return nil
		}
		lastErr = connErr

		// wait before the next attempt, but never beyond the timeout
		pause := 500 * time.Millisecond
		if remaining := timeout - time.Since(startTime); remaining < pause {
			pause = remaining
		}
		if pause > 0 {
			time.Sleep(pause)
		}
	}

	if lastErr != nil {
		return fmt.Errorf("Failed to reconnect within the %s timeout (last error: %w)", timeout, lastErr)
	}
	return fmt.Errorf("Failed to reconnect within the %s timeout", timeout)
}

// pgConnectWithRetry tries to connect to the PostgreSQL database. The whole
// attempt (connect + ping) is bound to the deadline of ctx.
func (pm *PGManager) pgConnectWithRetry(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, pm.connString())
	if err != nil {
		return err
	}

	err = conn.Ping(ctx)
	if err != nil {
		// don't leak the connection we just opened
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = conn.Close(closeCtx)
		cancel()
		return err
	}

	// the new connection replaces the broken one: release the old one
	if pm.conn != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = pm.conn.Close(closeCtx)
		cancel()
	}

	pm.conn = conn
	return nil
}
