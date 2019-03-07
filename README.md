# chatters
Microservice for [pajbot](https://github.com/pajlada/pajbot) to update chatters

## Usage

Copy `example.config.json` to `config.json` and edit the relevant fields.

```bash
go build
./chatters
```

## Full friendly instructions :D
This is assuming your `$GOPATH` is set. If it's not set, replace `$GOPATH` with wherever your go stuff gets installed. I think it's `~/go` by default  
### Install
```bash
go get github.com/pajlada/chatters
go install github.com/pajlada/chatters
```
### Make a folder to store your chatters config
```bash
mkdir /home/pajlada/chatters
```
### Set up the config
```bash
cp $GOPATH/src/github.com/pajlada/chatters/example.config.json /home/pajlada/chatters/config.json
cd /home/pajlada/chatters/
vim config.json
```
### Set up cronjob
```bash
crontab -e
```

Add the line: (replacing `/srv/go` with whatever your `$GOPATH` value is)
`*/10 * * * * cd /home/pajlada/chatters; /srv/go/bin/chatters >> /dev/null 2>>/dev/null`

### Alternative: systemd timer

copy `pajbot-chatters.timer` and `pajbot-chatters.service` to `/etc/systemd/system` and run as root (or with `sudo`):

```bash
systemctl daemon-reload
systemctl enable pajbot-chatters.timer
systemctl start pajbot-chatters.timer
```
