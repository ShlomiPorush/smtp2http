package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
	"github.com/alash3al/go-smtpsrv"
	"github.com/go-resty/resty/v2"
)

func main() {
	cfg := smtpsrv.ServerConfig{
		ReadTimeout:     time.Duration(*flagReadTimeout) * time.Second,
		WriteTimeout:    time.Duration(*flagWriteTimeout) * time.Second,
		ListenAddr:      *flagListenAddr,
		MaxMessageBytes: int(*flagMaxMessageSize),
		BannerDomain:    *flagServerName,
		Handler: smtpsrv.HandlerFunc(func(c *smtpsrv.Context) error {
			msg, err := c.Parse()
			if err != nil {
				return errors.New("Cannot read your message: " + err.Error())
			}

			spfResult, _, _ := c.SPF()

			// Initialize EmailMessage struct
			jsonData := EmailMessage{
				ID:            msg.MessageID,
				Date:          msg.Date.String(),
				References:    msg.References,
				SPFResult:     spfResult.String(),
				ResentDate:    msg.ResentDate.String(),
				ResentID:      msg.ResentMessageID,
				Subject:       msg.Subject,
				Attachments:   []*EmailAttachment{},
				EmbeddedFiles: []*EmailEmbeddedFile{},
			}

			// Decode email body content
			jsonData.Body.HTML, jsonData.Body.Text = decodeCharset(msg.HTMLBody, msg.TextBody)

			// Address handling
			jsonData.Addresses.From = transformStdAddressToEmailAddress([]*mail.Address{c.From()})[0]
			jsonData.Addresses.To = transformStdAddressToEmailAddress([]*mail.Address{c.To()})[0]

			toSplited := strings.Split(jsonData.Addresses.To.Address, "@")
			if len(*flagDomain) > 0 && (len(toSplited) < 2 || toSplited[1] != *flagDomain) {
				log.Println("domain not allowed")
				log.Println(*flagDomain)
				return errors.New("Unauthorized TO domain")
			}

			jsonData.Addresses.Cc = transformStdAddressToEmailAddress(msg.Cc)
			jsonData.Addresses.Bcc = transformStdAddressToEmailAddress(msg.Bcc)
			jsonData.Addresses.ReplyTo = transformStdAddressToEmailAddress(msg.ReplyTo)
			jsonData.Addresses.InReplyTo = msg.InReplyTo

			if resentFrom := transformStdAddressToEmailAddress(msg.ResentFrom); len(resentFrom) > 0 {
				jsonData.Addresses.ResentFrom = resentFrom[0]
			}

			jsonData.Addresses.ResentTo = transformStdAddressToEmailAddress(msg.ResentTo)
			jsonData.Addresses.ResentCc = transformStdAddressToEmailAddress(msg.ResentCc)
			jsonData.Addresses.ResentBcc = transformStdAddressToEmailAddress(msg.ResentBcc)

			for _, a := range msg.Attachments {
				data, _ := ioutil.ReadAll(a.Data)
				jsonData.Attachments = append(jsonData.Attachments, &EmailAttachment{
					Filename:    a.Filename,
					ContentType: a.ContentType,
					Data:        base64.StdEncoding.EncodeToString(data),
				})
			}

			for _, a := range msg.EmbeddedFiles {
				data, _ := ioutil.ReadAll(a.Data)
				jsonData.EmbeddedFiles = append(jsonData.EmbeddedFiles, &EmailEmbeddedFile{
					CID:         a.CID,
					ContentType: a.ContentType,
					Data:        base64.StdEncoding.EncodeToString(data),
				})
			}

			resp, err := resty.New().R().SetHeader("Content-Type", "application/json").SetBody(jsonData).Post(*flagWebhook)
			if err != nil {
				log.Println(err)
				return errors.New("E1: Cannot accept your message due to internal error, please report that to our engineers")
			} else if resp.StatusCode() != 200 {
				log.Println(resp.Status())
				return errors.New("E2: Cannot accept your message due to internal error, please report that to our engineers")
			}

			return nil
		}),
	}

	fmt.Println(smtpsrv.ListenAndServe(&cfg))
}

// decodeCharset decodes the email body from its charset
func decodeCharset(htmlBody, textBody string) (string, string) {
	htmlBodyDecoded, textBodyDecoded := htmlBody, textBody

	// Handle HTML body charset conversion if needed
	htmlCharset := "utf-8" // Default to UTF-8; adjust if needed
	if decodedBody, err := charset.NewReader(strings.NewReader(htmlBody), htmlCharset); err == nil {
		htmlBytes, _ := ioutil.ReadAll(decodedBody)
		htmlBodyDecoded = string(htmlBytes)
	}

	// Handle Text body charset conversion if needed
	textCharset := "utf-8" // Default to UTF-8; adjust if needed
	if decodedBody, err := charset.NewReader(strings.NewReader(textBody), textCharset); err == nil {
		textBytes, _ := ioutil.ReadAll(decodedBody)
		textBodyDecoded = string(textBytes)
	}

	return htmlBodyDecoded, textBodyDecoded
}
