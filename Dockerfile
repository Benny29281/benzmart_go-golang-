FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /benzmart ./cmd/monolith

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 benzmart
USER benzmart
COPY --from=build /benzmart /usr/local/bin/benzmart
EXPOSE 8080
ENTRYPOINT ["benzmart"]
