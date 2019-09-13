package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/op/go-logging"
	"gopkg.in/redis.v3"
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
	db             *sql.DB
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

func handleUsers(sql_tx *sql.Tx, redis_tx *redis.Multi, stream Stream, usernames []string) {
	// The script is currently set to run every 10 minutes
	interval := 10
	base_points := 2
	base_sub_points := 10

	if stream.BasePoints > 0 {
		base_points = stream.BasePoints
	}

	if stream.BasePointsSubbed > 0 {
		base_sub_points = stream.BasePointsSubbed
	}

	now := time.Now().Unix()
	last_seen_key := fmt.Sprintf("%s:users:last_seen", stream.Streamer)

	minutes_in_chat_online := 0
	minutes_in_chat_offline := 0

	if stream.Online {
		minutes_in_chat_online = interval
	} else {
		minutes_in_chat_offline = interval
	}

	for _, username := range usernames {
		var id int
		var subscriber int
		new := false
		err := sql_tx.QueryRow("SELECT id, subscriber FROM tb_user WHERE username = ?", username).Scan(&id, &subscriber)
		if err != nil {
			new = true
		}

		// Set last seen
		redis_tx.HSet(last_seen_key, username, strconv.FormatInt(now, 10))

		points_to_give := 0

		pointRate := base_points
		if subscriber == 1 {
			pointRate = base_sub_points
		}

		if stream.Online {
			points_to_give = pointRate
		} else {
			if stream.OfflineChatPointRate > 0.01 {
				points_to_give = int(math.Round(float64(float32(pointRate) * stream.OfflineChatPointRate)))
			}
		}

		if new {
			_, err := sql_tx.Exec(
				"INSERT INTO `tb_user` (`username`, `username_raw`, "+
					"`level`, `points`, `subscriber`, `minutes_in_chat_online`, "+
					"`minutes_in_chat_offline`) VALUES (?, ?, 100, ?, 0, ?, ?)",
				username,
				username,
				points_to_give,
				minutes_in_chat_online,
				minutes_in_chat_offline)
			if err != nil {
				log.Fatal(err)
			}
		} else {

			// TODO: Punish trump subs
			_, err := sql_tx.Exec(
				"UPDATE `tb_user` SET `points` = `points` + ?, "+
					"`minutes_in_chat_online` = `minutes_in_chat_online` + ?, "+
					"`minutes_in_chat_offline` = `minutes_in_chat_offline` + ? "+
					"WHERE `id` = ?",
				points_to_give,
				minutes_in_chat_online,
				minutes_in_chat_offline,
				id)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
}

func handleStream(stream Stream) {
	log.Debugf("Loading chatters for %s", stream.Streamer)
	// Initialize DB Connection for this stream
	stream.db, _ = sql.Open("postgres", stream.DataSourceName)

	if err := stream.db.Ping(); err != nil {
		log.Fatal(err)
	}

	// Check online status for streamer
	res, err := rclient.HGet("stream_data", fmt.Sprintf("%s:online", stream.Streamer)).Result()
	if err != nil {
		log.Fatal(err)
	}
	stream.Online = res == "True"

	// Load chatters JSON data
	url := fmt.Sprintf("https://tmi.twitch.tv/group/user/%s/chatters", stream.Streamer)
	resp, err := httpRequest(url)
	if err != nil {
		log.Error(err)
		return
	}
	var chatters ChattersList
	err = json.Unmarshal(resp, &chatters)
	if err != nil {
		log.Error(err)
		return
	}

	// Initialize database transaction
	sql_tx, err := stream.db.Begin()
	if err != nil {
		log.Fatal(err)
	}

	// Initialize redis pipeline
	redis_tx, err := rclient.Watch("stream_data") // XXX: Do I really need to watch something?
	if err != nil {
		log.Fatal(err)
	}
	defer redis_tx.Close()
	for _, value := range chatters.Chatters {
		handleUsers(sql_tx, redis_tx, stream, value)
	}

	// Commit all data
	sql_tx.Commit()
	log.Debugf("Updated data for %d chatters for streamer %s", chatters.ChatterCount, stream.Streamer)
}

func main() {
	// Initialize logging
	backend1 := logging.NewLogBackend(os.Stdout, "", 0)
	backend2 := logging.NewLogBackend(os.Stdout, "", 0)
	backend2Formatter := logging.NewBackendFormatter(backend2, format)
	backend1Leveled := logging.AddModuleLevel(backend1)
	backend1Leveled.SetLevel(logging.ERROR, "")
	logging.SetBackend(backend1Leveled, backend2Formatter)

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

	for _, stream := range config.Streams {
		go func(stream Stream) {
			defer wg.Done()
			handleStream(stream)
		}(stream)
	}

	wg.Wait()
	log.Debug("Done updating chatters")
}
