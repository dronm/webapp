module github.com/dronm/webapp

go 1.25.0

require (
	github.com/dronm/modelbind v0.0.0-20260504034703-7290b55279cc
	github.com/dronm/session v0.0.0-20251220011348-444cd0bd6dad
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.9.2
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dronm/ds/v4 v4.0.0-20260612024620-1d1425e7a834 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/redis/go-redis/v9 v9.17.2 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/dronm/ds/v4 => /home/andrey/go/ds/v4

replace github.com/dronm/session => /home/andrey/go/session

replace github.com/dronm/modelbind => /home/andrey/go/modelbind
