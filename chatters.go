package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/op/go-logging"
)

var (
	log    = logging.MustGetLogger("chatters")
	format = logging.MustStringFormatter(
		`%{color}[%{time:2006-01-02 15:04:05.000}] [%{level:.4s}] %{color:reset}%{message}`,
	)
	rclient *redis.Client
)

type Stream struct {
	Streamer       string `json:"streamer"`
	DataSourceName string `json:"dsn"`
	db             *sqlx.DB
	Online         bool

	BasePoints       int `json:"base_points"`
	BasePointsSubbed int `json:"base_points_subbed"`

	// OfflineChatPointRate specifies how fast offline chatters should gain points, if at all.
	// 0.0 by default which means no points for offline chatters
	OfflineChatPointRate float32 `json:"offline_chat_point_rate"`
}

type Config struct {
	Streams []Stream `json:"streams"`
}

type ChattersList struct {
	ChatterCount int                 `json:"chatter_count"`
	Chatters     map[string][]string `json:"chatters"`
}

func httpRequest(url string) ([]byte, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	contents, err := ioutil.ReadAll(response.Body)
	if err != nil {
		log.Error(err)
		return nil, err
	}
	return contents, nil
}

func handleUsers(sql_tx *sqlx.Tx, redis_tx redis.Pipeliner, stream Stream, chatters *ChattersList) error {
	_, err := sql_tx.Exec("CREATE TEMPORARY TABLE chatters(username TEXT PRIMARY KEY NOT NULL) ON COMMIT DROP")
	if err != nil {
		return err
	}

	for _, chatterCategory := range chatters.Chatters {
		for _, username := range chatterCategory {
			_, err := sql_tx.Exec("INSERT INTO chatters VALUES (?)", username)
			if err != nil {
				return err
			}
		}
	}

	// The script is currently set to run every 10 minutes
	update_interval := 10
	stream_online := stream.Online
	base_points := 2
	base_sub_points := 10
	offline_point_rate := stream.OfflineChatPointRate

	if stream.BasePoints > 0 {
		base_points = stream.BasePoints
	}

	if stream.BasePointsSubbed > 0 {
		base_sub_points = stream.BasePointsSubbed
	}

	_, err = sql_tx.Exec(`
INSERT INTO "user"(username, username_raw, level, points, subscriber, minutes_in_chat_online, minutes_in_chat_offline)
    (SELECT chatters.username AS username,
            chatters.username AS username_raw,
            100 AS level,
            ? AS points,
            FALSE AS subscriber,
            CASE WHEN ? THEN ? ELSE 0 END AS minutes_in_chat_online,
            CASE WHEN NOT ? THEN ? ELSE 0 END AS minutes_in_chat_offline
     FROM chatters)
ON CONFLICT (username) DO UPDATE SET
    points = "user".points + round(
        CASE WHEN "user".subscriber THEN ? ELSE ? END *
        CASE WHEN ? THEN 1 ELSE ? END
    ),
    minutes_in_chat_online  = "user".minutes_in_chat_online + CASE WHEN ? THEN ? ELSE 0 END,
    minutes_in_chat_offline = "user".minutes_in_chat_offline + CASE WHEN NOT ? THEN ? ELSE 0 END
`, base_points, stream_online, update_interval, stream_online, update_interval, base_sub_points,
		base_points, stream_online, offline_point_rate,
		stream_online, update_interval, stream_online, update_interval)
	if err != nil {
		return err
	}

	now_formatted := strconv.FormatInt(time.Now().Unix(), 10)
	last_seen_key := fmt.Sprintf("%s:users:last_seen", stream.Streamer)

	multiset_args := make(map[string]interface{})
	for _, chatterCategory := range chatters.Chatters {
		for _, username := range chatterCategory {
			multiset_args[username] = now_formatted
		}
	}
	redis_tx.HMSet(last_seen_key, multiset_args)

	return nil
}

func handleStream(stream Stream) error {
	log.Debugf("Loading chatters for %s", stream.Streamer)
	// Initialize DB Connection for this stream
	db, err := sqlx.Connect("postgres", stream.DataSourceName)
	if err != nil {
		return err
	}
	stream.db = db

	// Check online status for streamer
	res, err := rclient.HGet("stream_data", fmt.Sprintf("%s:online", stream.Streamer)).Result()
	if err != nil {
		return err
	}
	stream.Online = res == "True"

	// Load chatters JSON data
	url := fmt.Sprintf("https://tmi.twitch.tv/group/user/%s/chatters", stream.Streamer)
	resp, err := httpRequest(url)
	if err != nil {
		return err
	}
	var chatters ChattersList
	err = json.Unmarshal(resp, &chatters)
	if err != nil {
		return err
	}

	// Initialize database transaction
	err = WithTransaction(stream.db, func(sql_tx *sqlx.Tx) error {
		// Initialize redis MULTI pipeline
		_, err = rclient.TxPipelined(func(pipe redis.Pipeliner) error {
			err := handleUsers(sql_tx, pipe, stream, &chatters)
			if err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	log.Debugf("Updated data for %d chatters for streamer %s", chatters.ChatterCount, stream.Streamer)

	return nil
}

func main() {
	// Initialize logging
	backend := logging.NewLogBackend(os.Stdout, "", 0)
	backendFormatter := logging.NewBackendFormatter(backend, format)
	logging.SetBackend(backendFormatter)

	log.Debug("Starting chatters update")

	// Connect to redis
	rclient = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	// Read config file
	file, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal(err)
	}
	var config Config
	err = json.Unmarshal(file, &config)
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(len(config.Streams))

	exitCode := 0
	for _, stream := range config.Streams {
		go func(stream Stream) {
			defer wg.Done()
			err := handleStream(stream)
			if err != nil {
				log.Errorf("Error fetching stream data for %s: %s", stream.Streamer, err)
				exitCode = 1
			}
		}(stream)
	}

	wg.Wait()
	log.Debug("Done updating chatters")
	os.Exit(exitCode)
}
