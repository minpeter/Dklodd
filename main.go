package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
)

func main() {

	router := gin.Default()
	LoadOnlineSandbox()

	router.LoadHTMLGlob("templates/components/*")

	_, err := client.NewClientWithOpts()
	if err != nil {
		fmt.Println("Docker Client Error: ", err)
	}

	router.GET("/", func(c *gin.Context) {
		chall, _ := GetAllChall()

		RenderTemplates(c, gin.H{
			"challenges": chall,
		})
	})

	router.GET("/:id", func(c *gin.Context) {
		id := c.Param("id")
		chall := GetChallbyId(id)

		RenderTemplates(c, chall, "challenge")
	})

	router.Static("/assets", "templates/assets")

	router.GET("/:id/new", create)
	router.GET("/:id/del", remove)

	// 어드민 전용 라우터 생성
	admin := router.Group("/admin", gin.BasicAuth(gin.Accounts{
		"admin": "admin",
	}))

	adminRouter(admin)

	env := os.Getenv("PORT")
	if env == "" {
		env = "8000"
	}

	host := ":" + env
	// Removes the “accept incoming network connections?” pop-up on macOS.
	if runtime.GOOS == "darwin" {
		host = "localhost:" + env
	}

	log.Fatal(router.Run(host))
}

func create(c *gin.Context) {
	cli, err := client.NewClientWithOpts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "docker client error - 1",
		})
		return
	}

	challengeID := c.Param("id")

	host := strings.Split(c.Request.Host, ":")

	if len(host) == 1 {
		if c.Request.TLS != nil || c.Request.Header.Get("X-Forwarded-Proto") == "https" || strings.Contains(c.Request.Referer(), "https") {
			// HTTPS인 경우 443번 포트로 설정
			host = append(host, "443")
		} else {
			// HTTP인 경우 80번 포트로 설정
			host = append(host, "80")
		}
	}

	// get hostname from url

	if challengeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "id is empty",
		})
		return
	}

	ctx := context.Background()

	chall := GetChallbyId(challengeID)
	imageName := chall.Image

	fmt.Println("create sandbox: " + imageName)

	hashId := GenerateId(c)

	_, _, err = cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		fmt.Println("pull image: " + imageName)
		out, err := cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": "docker client error - fail to pull image",
			})
			return
		}

		// Wait for the image pull to complete
		var buf bytes.Buffer
		_, copyErr := io.Copy(&buf, out)
		if copyErr != nil {
			// Handle the copy error
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": "docker client error - fail to read image pull output",
			})
			return
		}

		// Check if there are any errors reported in the output
		if strings.Contains(buf.String(), "error") {
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": "docker client error - error in image pull output",
			})
			return
		}

		// Now the image pull is complete
		fmt.Println("Image pull complete for: " + imageName)
	}

	config := &container.Config{
		Image: imageName,
		Labels: map[string]string{
			"traefik.enable": "true",
			"traefik.tcp.routers." + hashId + ".rule": "HostSNI(`" + hashId + "." + host[0] + "`)",
			"traefik.tcp.routers." + hashId + ".tls":  "true",
			"dklodd":                                  "true",
		},
		Env: chall.Env,
	}

	if chall.Type == "web" {
		config.Labels = map[string]string{
			"traefik.enable": "true",
			"traefik.http.routers." + hashId + ".rule": "Host(`" + hashId + "." + host[0] + "`)",
			"traefik.http.routers." + hashId + ".tls":  "true",
			"dklodd": "true",
		}
	}

	hostConfig := &container.HostConfig{
		NetworkMode: "traefik",
	}

	resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "docker client error - 2",
		})
		return
	}

	sandboxID := resp.ID

	// Start the container
	if err := cli.ContainerStart(ctx, sandboxID, types.ContainerStartOptions{}); err != nil {
		fmt.Println("Failed to start container:", err) // 에러 메시지 출력
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "docker client error - 3: failed to start container",
		})
		return
	}

	fmt.Println("create sandbox: " + sandboxID[0:12])

	OnlineSandboxIds = append(OnlineSandboxIds, sandboxID[0:12])

	if chall.Type == "web" {

		connection := "https://" + hashId + "." + host[0]

		if host[1] != "443" {
			connection += ":" + host[1]
		}

		c.HTML(http.StatusOK, "web.tmpl", gin.H{
			"Connection": connection,
			"Id":         sandboxID[0:12],
		})
	} else {
		c.HTML(http.StatusOK, "tcp.tmpl", gin.H{
			"Connection": []struct {
				Type    string
				Command string
			}{
				{
					Type:    "ncat",
					Command: "ncat --ssl " + hashId + "." + host[0] + " " + host[1],
				},
				{
					Type:    "openssl",
					Command: "openssl s_client -connect " + hashId + "." + host[0] + ":" + host[1],
				},
				{
					Type:    "socat",
					Command: "socat openssl:" + hashId + "." + host[0] + ":" + host[1] + ",verify=0 -",
				},
				{
					Type:    "gnutls",
					Command: "gnutls-cli --insecure " + hashId + "." + host[0] + ":" + host[1],
				},
				{
					Type:    "pwn",
					Command: "remote('" + hashId + "." + host[0] + "', " + host[1] + ", ssl=True)",
				},
			},
			"Id": sandboxID[0:12],
		})
	}
}

func remove(c *gin.Context) {

	sandboxId := c.Param("id")

	cli, err := client.NewClientWithOpts()
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	var message string

	for _, onlineSandboxId := range OnlineSandboxIds {
		if onlineSandboxId == sandboxId {
			if err := cli.ContainerStop(ctx, sandboxId, nil); err != nil {
				message = "docker client error - 3: failed to stop container"
				break
			}

			if err := cli.ContainerRemove(ctx, sandboxId, types.ContainerRemoveOptions{
				RemoveVolumes: true,
				Force:         true,
			}); err != nil {
				message = "docker client error - 4: failed to remove container"
				break
			}

			for i, onlineSandboxId := range OnlineSandboxIds {
				if onlineSandboxId == sandboxId {
					OnlineSandboxIds = append(OnlineSandboxIds[:i], OnlineSandboxIds[i+1:]...)
				}
			}

			message = "successfully removed sandbox"
			break
		}
	}

	if message == "" {
		message = "sandbox not found"
	}

	fmt.Println(message)

	id := c.GetHeader("HX-Current-URL")
	id = strings.Split(id, "/")[len(strings.Split(id, "/"))-1]

	chall := GetChallbyId(id)

	chall.Message = message + " - " + sandboxId

	RenderTemplates(c, chall, "challenge")
}
