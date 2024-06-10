# Use the latest Go image
FROM golang:latest

# Set the working directory
WORKDIR /app

# Copy all files to the container
COPY . .

# Build the Go application
RUN go build -o main main.go