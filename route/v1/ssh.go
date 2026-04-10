package v1

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/common_err"
	sshHelper "github.com/NimoTech/NimoOS-Common/utils/ssh"
	"github.com/NimoTech/NimoOS/pkg/utils"
	"github.com/labstack/echo/v4"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	modelCommon "github.com/NimoTech/NimoOS-Common/model"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	CheckOrigin:      func(r *http.Request) bool { return true },
	HandshakeTimeout: time.Duration(time.Second * 5),
}

// WsSsh opens an interactive WebSocket terminal session.
//
// Authentication flow (interactive, no pre-login form):
//  1. The frontend passes the NimoOS username via the `username` query param.
//     The password is NOT passed — it is collected interactively inside the
//     terminal stream so the experience feels like a native Linux TTY.
//  2. The handler sends a "Password: " prompt over the WebSocket.
//  3. The user types their Linux password (chars are not echoed back).
//  4. An SSH connection is attempted with the provided credentials.
//     - If the Linux user's shell is /bin/false (non-admin), Session.Shell()
//       fails naturally — this is the access control mechanism.
//     - If the password is wrong, "Permission denied" is shown and the
//       password prompt repeats (up to maxRetries times).
func WsSsh(ctx echo.Context) error {
	userName := ctx.QueryParam("username")
	port := utils.DefaultQuery(ctx, "port", "22")
	cols, _ := strconv.Atoi(utils.DefaultQuery(ctx, "cols", "200"))
	rows, _ := strconv.Atoi(utils.DefaultQuery(ctx, "rows", "32"))

	wsConn, err := upgrader.Upgrade(ctx.Response().Writer, ctx.Request(), nil)
	if err != nil {
		return nil
	}
	defer wsConn.Close()

	logBuff := new(bytes.Buffer)

	const maxRetries = 3
	var client *ssh.Client

	// Wait a moment to ensure the frontend xterm is ready (open and addon loaded)
	// Show account info header like a real Linux login
	hostname, _ := os.Hostname()
	if userName == "" {
		userName = "admin"
	}

	attemptCount := 0
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Prompt for password
		loginPrompt := fmt.Sprintf("\r\n%s@%s: ~\r\nPassword: ", userName, hostname)
		wsConn.WriteMessage(websocket.TextMessage, []byte(loginPrompt))

		// Collect the password silently
		password := sshHelper.ReceiveWsMsgPassword(wsConn, logBuff, &attemptCount)
		if password == "" {
			// Connection closed by client while waiting for input.
			return nil
		}

		// Move to a new line after password entry (password itself is not echoed)
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\n"))

		// Attempt SSH connection.
		// If the Linux user has /bin/false as their shell, NewSshClient will
		// succeed but NewSshConn (Session.Shell()) will fail — natural access control.
		client, err = sshHelper.NewSshClient(userName, password, port)
		if err == nil {
			break // Authentication succeeded.
		}

		// Authentication failed — notify and retry.
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\nPermission denied, please try again.\r\n"))
	}

	if client == nil {
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\nToo many authentication failures.\r\n"))
		return nil
	}
	defer client.Close()

	// Open PTY shell. Fails naturally if the user's shell is /bin/false.
	ssConn, err := sshHelper.NewSshConn(cols, rows, client)
	if err != nil {
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\nPermission denied: unable to open shell.\r\n"))
		return nil
	}
	defer ssConn.Close()

	quitChan := make(chan bool, 3)
	go ssConn.ReceiveWsMsg(wsConn, logBuff, quitChan)
	go ssConn.SendComboOutput(wsConn, quitChan)
	go ssConn.SessionWait(quitChan)

	<-quitChan
	return nil
}

// PostSshLogin is kept for backward-compatibility but is no longer used by the
// main terminal flow. The new flow authenticates interactively inside WsSsh.
func PostSshLogin(ctx echo.Context) error {
	return ctx.JSON(http.StatusGone, modelCommon.Result{
		Success: common_err.SERVICE_ERROR,
		Message: "This endpoint is deprecated. Authentication is now handled interactively inside the WebSocket terminal.",
	})
}
