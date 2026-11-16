# Runs the grasp MCP server over stdio. Mount your project at /project:
#   docker run -i --rm -v "$PWD:/project" grasp
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /grasp ./cmd/grasp

FROM alpine:3.22
RUN apk add --no-cache git
COPY --from=build /grasp /usr/local/bin/grasp
WORKDIR /project
ENTRYPOINT ["grasp", "mcp", "-dir", "/project"]
