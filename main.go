package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/alash3al/go-smtpsrv"
	"github.com/go-resty/resty/v2"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

func main() {
	// Define your flags
	flagReadTimeout := flag.Int("read-timeout", 10, "Read timeout in seconds")
	flagWriteTimeout := flag.Int("write-timeout", 10, "Write timeout in seconds")
	flagListenAddr := flag.String("listen-addr", ":1025", "SMTP server listen address")
	flagMaxMessageSize := flag.Int("max-message-size", 1024*1024, "Max message size in bytes")
	flagServerName := flag.String("server-name", "localhost", "SMTP server banner domain")
	flagDomain := flag.String("domain", "", "Allowed TO domain")
	flagWebhook := flag.String("webhook", "", "Webhook URL to post messages")

	flag.Parse()

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

			// Decode subject if it's encoded in MIME format
			decodedSubject, err := decodeMIMEHeader(msg.Subject)
			if err != nil {
				log.Println("Failed to decode subject:", err)
				decodedSubject = msg.Subject // Fallback to raw subject
			}

			jsonData := EmailMessage{
				ID:            msg.MessageID,
				Date:          msg.Date.String(),
				References:    msg.References,
				SPFResult:     spfResult.String(),
				ResentDate:    msg.ResentDate.String(),
				ResentID:      msg.ResentMessageID,
				Subject:       decodedSubject,
				Attachments:   []*EmailAttachment{},
				EmbeddedFiles: []*EmailEmbeddedFile{},
			}

			// Decode the HTML and Text bodies properly
			jsonData.Body.HTML, err = decodeCharset(msg.HTMLBody, msg.HTMLCharset)
			if err != nil {
				log.Println("Failed to decode HTML body:", err)
				jsonData.Body.HTML = string(msg.HTMLBody) // Fallback to raw body
			}

			jsonData.Body.Text, err = decodeCharset(msg.TextBody, msg.TextCharset)
			if err != nil {
				log.Println("Failed to decode Text body:", err)
				jsonData.Body.Text = string(msg.TextBody) // Fallback to raw body
			}

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

// decodeMIMEHeader decodes MIME encoded words like `=?windows-1255?B?...?=`
func decodeMIMEHeader(encoded string) (string, error) {
	// Check if the subject uses MIME encoding syntax
	if strings.HasPrefix(encoded, "=?") && strings.HasSuffix(encoded, "?=") {
		sections := strings.Split(encoded, "?")
		if len(sections) != 5 {
			return "", errors.New("invalid MIME encoding format")
		}
		charset := strings.ToLower(sections[1])
		encoding := strings.ToLower(sections[2])
		encodedText := sections[3]

		// Decode the base64 content
		if encoding == "b" {
			decodedBytes, err := base64.StdEncoding.DecodeString(encodedText)
			if err != nil {
				return "", err
			}

			// Convert charset to UTF-8
			decodedText, err := convertToUTF8(decodedBytes, charset)
			if err != nil {
				return "", err
			}

			return decodedText, nil
		}
	}

	return encoded, nil // Return the raw string if not MIME encoded
}

// convertToUTF8 converts the byte array from the specified charset to UTF-8
func convertToUTF8(data []byte, charsetName string) (string, error) {
	encoding, name := getEncodingByName(charsetName)
	if name != "utf-8" {
		reader := transform.NewReader(bytes.NewReader(data), encoding.NewDecoder())
		decodedBody, err := ioutil.ReadAll(reader)
		if err != nil {
			return "", err
		}
		return string(decodedBody), nil
	}

	// Return the original body if it's already UTF-8
	return string(data), nil
}

// getEncodingByName returns the encoding object by name
func getEncodingByName(name string) (encoding.Encoding, string) {
	switch strings.ToLower(name) {
	case "windows-1255":
		return charset.Charset("windows-1255"), "windows-1255"
	// Add more encodings as needed
	default:
		return charset.UTF8, "utf-8"
	}
}

// decodeCharset decodes the body from a given charset to UTF-8
func decodeCharset(encodedBody []byte, charsetName string) (string, error) {
	return convertToUTF8(encodedBody, charsetName)
}
