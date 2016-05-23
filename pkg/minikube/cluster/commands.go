package cluster

var startCommand = `
# Run with nohup so it stays up. Redirect logs to useful places.
PATH=/usr/local/sbin:$PATH nohup sudo /usr/local/bin/localkube start --generate-certs=false > /var/log/localkube.out 2> /var/log/localkube.err < /dev/null &
`
