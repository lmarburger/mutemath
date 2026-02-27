FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY core/ ./core/
RUN CGO_ENABLED=0 go build -o /mutemath .

FROM alpine:3
RUN apk add --no-cache ca-certificates
COPY --from=build /mutemath /usr/local/bin/
ENTRYPOINT ["mutemath"]
