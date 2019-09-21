# chatters
Microservice for [pajbot](https://github.com/pajbot/pajbot) to update chatters.

- Award your users with points for watching the stream and/or being in offline chat
- Update when users were last seen
- Update how many minutes your users spent in online/offline chat

## Install

Clone and build (You can install go with `sudo apt install golang-go` if you are missing it):

```bash
git clone https://github.com/pajbot/chatters.git
cd chatters
go build
```

Copy `config.json.example` to `config.json` and edit the relevant fields:

```bash
cp config.example.json
editor config.json
```

Move service files to `/opt`:

```bash
sudo cp -r . /opt/pajbot-chatters
sudo chown -R pajbot:pajbot /opt/pajbot-chatters
```

And activate the timer to run the service every 10 minutes.

```
sudo cp pajbot-chatters.service pajbot-chatters.timer /etc/systemd/system
sudo systemctl daemon-reload
sudo systemctl enable --now pajbot-chatters.timer
```
