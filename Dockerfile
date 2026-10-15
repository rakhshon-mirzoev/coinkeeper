FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
ARG APP=api
RUN CGO_ENABLED=0 go build -o /out/app ./cmd/${APP}

FROM golang:1.26-alpine AS goose
RUN CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@v3.29.0

FROM alpine:3.20 AS migrate
RUN adduser -D -u 10001 app
USER app
COPY --from=goose /go/bin/goose /usr/local/bin/goose
COPY migrations /migrations
ENTRYPOINT ["goose"]
CMD ["up"]

# Финальный этап идет последним
FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
