FROM golang:1.23-alpine

WORKDIR /app
COPY . .

RUN go get github.com/tarantool/go-tarantool/v2
RUN go get github.com/tarantool/go-tarantool/v2/datetime
RUN go get github.com/tarantool/go-tarantool/v2/decimal
RUN go get github.com/tarantool/go-tarantool/v2/uuid
RUN go get github.com/mattermost/mattermost-server/v6/model
RUN go get github.com/joho/godotenv
RUN go get github.com/rs/zerolog

RUN go build -o bot .

CMD ["./bot"]
