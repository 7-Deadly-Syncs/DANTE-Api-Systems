package legacy

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
)

const (
	soapEnvelopeNamespace = "http://schemas.xmlsoap.org/soap/envelope/"
	soapServiceNamespace  = "http://BankService.services.axis2"
)

// Client translates DANTE domain operations into legacy SOAP BankService calls.
type Client struct {
	endpoint   string
	httpClient *http.Client
}

// MerchantRecord is the typed legacy representation returned by getQrisMerchant.
type MerchantRecord struct {
	Code string
	Name string
}

// BankDetails contains bank-level metadata returned by the legacy system.
type BankDetails struct {
	Type string
	Code string
	Name string
}

// RegisterResult contains the authoritative registration result returned by the legacy system.
type RegisterResult struct {
	CustomerID    string
	AccountID     string
	AccountNumber string
}

// LoginResult contains the authoritative login result returned by the legacy system.
type LoginResult struct {
	CustomerID       string
	AccountID        string
	AccountNumber    string
	CustomerName     string
	SessionReference string
	ExpiresAt        time.Time
}

// AccountProfile contains the profile data returned by the legacy system.
type AccountProfile struct {
	CustomerID    string
	AccountID     string
	AccountNumber string
	Name          string
}

// BalanceSnapshot contains the balance response returned by the legacy system.
type BalanceSnapshot struct {
	AccountID          string
	ReferenceAccountID string
	Balance            int64
}

// TransferResult contains the transfer result returned by the legacy system.
type TransferResult struct {
	FromAccount string
	ToAccount   string
	Amount      int64
}

// QRISPaymentResult contains the QRIS payment result returned by the legacy system.
type QRISPaymentResult struct {
	AccountID    string
	MerchantCode string
	Amount       int64
}

// OperationError reports a non-success response returned by the legacy system.
type OperationError struct {
	Operation string
	Response  string
}

func (e *OperationError) Error() string {
	return fmt.Sprintf("legacy %s failed: %s", e.Operation, e.Response)
}

// NewClient constructs a SOAP client from runtime config.
func NewClient(cfg config.LegacyConfig) *Client {
	return &Client{
		endpoint: strings.TrimSpace(cfg.BankServiceURL),
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Endpoint returns the configured SOAP service endpoint.
func (c *Client) Endpoint() string {
	return c.endpoint
}

// Ping verifies that the SOAP service endpoint is reachable.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.StartClientSpan(ctx, "legacy", "legacy.ping",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "wsdl"),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if strings.TrimSpace(c.endpoint) == "" {
		spanErr = ErrUnavailable
		return ErrUnavailable
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.wsdlURL(), nil)
	if err != nil {
		spanErr = err
		return fmt.Errorf("build legacy ping request: %w", err)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		spanErr = err
		return fmt.Errorf("call legacy wsdl: %w", err)
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		spanErr = fmt.Errorf("legacy wsdl returned status %d", resp.StatusCode)
		return spanErr
	}

	return nil
}

// GetBankDetails returns bank metadata from the legacy service.
func (c *Client) GetBankDetails(ctx context.Context, email, password string) (*BankDetails, error) {
	parts, err := c.callAndRequireOK(ctx, "getBankDetails", email, password)
	if err != nil {
		return nil, err
	}
	if len(parts) < 4 {
		return nil, fmt.Errorf("legacy getBankDetails returned incomplete response: %q", strings.Join(parts, "|"))
	}

	return &BankDetails{
		Type: parts[1],
		Code: parts[2],
		Name: parts[3],
	}, nil
}

// Register creates a new customer account in the legacy system.
func (c *Client) Register(ctx context.Context, name, email, password, pin string) (*RegisterResult, error) {
	parts, err := c.callAndRequireOK(ctx, "register", name, email, password, pin)
	if err != nil {
		return nil, err
	}
	if len(parts) < 4 {
		return nil, fmt.Errorf("legacy register returned incomplete response: %q", strings.Join(parts, "|"))
	}

	return &RegisterResult{
		CustomerID:    parts[1],
		AccountID:     parts[2],
		AccountNumber: parts[3],
	}, nil
}

// Login validates credentials with the legacy system.
func (c *Client) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	parts, err := c.callAndRequireOK(ctx, "login", email, password)
	if err != nil {
		return nil, err
	}
	if len(parts) < 6 {
		return nil, fmt.Errorf("legacy login returned incomplete response: %q", strings.Join(parts, "|"))
	}

	expiresUnix, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse legacy login expiry: %w", err)
	}

	result := &LoginResult{
		CustomerID:       parts[1],
		AccountID:        parts[2],
		SessionReference: parts[4],
		ExpiresAt:        time.Unix(expiresUnix, 0).UTC(),
	}
	if len(parts) >= 4 {
		// The current legacy wrapper leaks the account number in slot 3.
		result.AccountNumber = parts[3]
	}

	return result, nil
}

// Logout invalidates a legacy session reference.
func (c *Client) Logout(ctx context.Context, sessionID string) error {
	parts, err := c.callAndRequireOK(ctx, "logout", sessionID)
	if err != nil {
		return err
	}
	if len(parts) < 2 || parts[1] != "LOGOUT" {
		return fmt.Errorf("legacy logout returned unexpected response: %q", strings.Join(parts, "|"))
	}

	return nil
}

// GetAccountProfile returns the authoritative account profile from legacy.
func (c *Client) GetAccountProfile(ctx context.Context, accountID, password string) (*AccountProfile, error) {
	parts, err := c.callAndRequireOK(ctx, "getAccountProfile", accountID, password)
	if err != nil {
		return nil, err
	}
	if len(parts) < 5 {
		return nil, fmt.Errorf("legacy getAccountProfile returned incomplete response: %q", strings.Join(parts, "|"))
	}

	return &AccountProfile{
		CustomerID:    parts[1],
		AccountID:     parts[2],
		AccountNumber: parts[3],
		Name:          parts[4],
	}, nil
}

// GetBalance returns the authoritative balance snapshot from legacy.
func (c *Client) GetBalance(ctx context.Context, accountID, pin string) (*BalanceSnapshot, error) {
	parts, err := c.callAndRequireOK(ctx, "balance", accountID, pin)
	if err != nil {
		return nil, err
	}
	if len(parts) < 4 {
		return nil, fmt.Errorf("legacy balance returned incomplete response: %q", strings.Join(parts, "|"))
	}

	balance, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse legacy balance amount: %w", err)
	}

	return &BalanceSnapshot{
		AccountID:          parts[1],
		ReferenceAccountID: parts[2],
		Balance:            balance,
	}, nil
}

// Transfer executes a transfer in the legacy system.
func (c *Client) Transfer(ctx context.Context, fromAccount, pin, toAccount string, amount int64) (*TransferResult, error) {
	parts, err := c.callAndRequireOK(ctx, "transfer", fromAccount, pin, toAccount, strconv.FormatInt(amount, 10))
	if err != nil {
		return nil, err
	}
	if len(parts) < 4 {
		return nil, fmt.Errorf("legacy transfer returned incomplete response: %q", strings.Join(parts, "|"))
	}

	parsedAmount, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse legacy transfer amount: %w", err)
	}

	return &TransferResult{
		FromAccount: parts[1],
		ToAccount:   parts[2],
		Amount:      parsedAmount,
	}, nil
}

// PayQRIS executes a QRIS payment in the legacy system.
func (c *Client) PayQRIS(ctx context.Context, accountID, merchantCode string, amount int64) (*QRISPaymentResult, error) {
	parts, err := c.callAndRequireOK(ctx, "qris", accountID, merchantCode, strconv.FormatInt(amount, 10))
	if err != nil {
		return nil, err
	}
	if len(parts) < 4 {
		return nil, fmt.Errorf("legacy qris returned incomplete response: %q", strings.Join(parts, "|"))
	}

	parsedAmount, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse legacy qris amount: %w", err)
	}

	return &QRISPaymentResult{
		AccountID:    parts[1],
		MerchantCode: parts[2],
		Amount:       parsedAmount,
	}, nil
}

// GetQrisMerchant returns merchant data from the legacy system by merchant code.
func (c *Client) GetQrisMerchant(ctx context.Context, merchantCode string) (*MerchantRecord, error) {
	parts, err := c.callAndRequireOK(ctx, "getQrisMerchant", merchantCode)
	if err != nil {
		if opErr := new(OperationError); errors.As(err, &opErr) && isNotFoundResponse(opErr.Response) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(parts) < 3 {
		return nil, fmt.Errorf("legacy getQrisMerchant returned incomplete response: %q", strings.Join(parts, "|"))
	}

	return &MerchantRecord{
		Code: parts[1],
		Name: parts[2],
	}, nil
}

func (c *Client) callAndRequireOK(ctx context.Context, operation string, args ...string) ([]string, error) {
	raw, err := c.callOperation(ctx, operation, args...)
	if err != nil {
		return nil, err
	}

	parts := splitLegacyResponse(raw)
	if len(parts) == 0 {
		return nil, fmt.Errorf("legacy %s returned empty response", operation)
	}
	if !strings.EqualFold(parts[0], "OK") {
		return nil, &OperationError{
			Operation: operation,
			Response:  raw,
		}
	}

	return parts, nil
}

func (c *Client) callOperation(ctx context.Context, operation string, args ...string) (string, error) {
	ctx, span := tracing.StartClientSpan(ctx, "legacy", "legacy.soap "+operation,
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", operation),
		attribute.String("rpc.system", "soap"),
		attribute.String("rpc.method", operation),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, ErrNotFound)
	}()

	if strings.TrimSpace(c.endpoint) == "" {
		spanErr = ErrUnavailable
		return "", ErrUnavailable
	}

	payload := buildSOAPRequest(operation, args...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		spanErr = err
		return "", fmt.Errorf("build legacy %s request: %w", operation, err)
	}

	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", operation)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		spanErr = err
		return "", fmt.Errorf("call legacy %s: %w", operation, err)
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		spanErr = err
		return "", fmt.Errorf("read legacy %s response: %w", operation, err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		spanErr = fmt.Errorf("legacy %s returned status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(body)))
		return "", spanErr
	}

	result, err := parseSOAPReturn(body)
	if err != nil {
		spanErr = err
		return "", fmt.Errorf("parse legacy %s response: %w", operation, err)
	}

	return result, nil
}

func (c *Client) wsdlURL() string {
	parsed, err := url.Parse(c.endpoint)
	if err != nil {
		return c.endpoint + "?wsdl"
	}

	query := parsed.Query()
	query.Set("wsdl", "")
	parsed.RawQuery = query.Encode()

	wsdlURL := parsed.String()
	return strings.Replace(wsdlURL, "wsdl=", "wsdl", 1)
}

func buildSOAPRequest(operation string, args ...string) []byte {
	var buf strings.Builder
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	buf.WriteString(`<soapenv:Envelope xmlns:soapenv="`)
	buf.WriteString(soapEnvelopeNamespace)
	buf.WriteString(`" xmlns:ns="`)
	buf.WriteString(soapServiceNamespace)
	buf.WriteString(`"><soapenv:Header/><soapenv:Body><ns:`)
	buf.WriteString(operation)
	buf.WriteString(`>`)

	for idx, arg := range args {
		buf.WriteString(`<ns:args`)
		buf.WriteString(strconv.Itoa(idx))
		buf.WriteString(`>`)
		buf.WriteString(html.EscapeString(arg))
		buf.WriteString(`</ns:args`)
		buf.WriteString(strconv.Itoa(idx))
		buf.WriteString(`>`)
	}

	buf.WriteString(`</ns:`)
	buf.WriteString(operation)
	buf.WriteString(`></soapenv:Body></soapenv:Envelope>`)
	return []byte(buf.String())
}

func parseSOAPReturn(body []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))

	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		switch start.Name.Local {
		case "return":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return "", err
			}
			return strings.TrimSpace(value), nil
		case "faultstring":
			var fault string
			if err := decoder.DecodeElement(&fault, &start); err != nil {
				return "", err
			}
			return "", errors.New(strings.TrimSpace(fault))
		}
	}

	return "", errors.New("legacy soap response missing return value")
}

func splitLegacyResponse(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	rawParts := strings.Split(trimmed, "|")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		parts = append(parts, strings.TrimSpace(part))
	}

	return parts
}

func isNotFoundResponse(raw string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(raw))
	return strings.Contains(normalized, "NOT_FOUND") || strings.Contains(normalized, "NOT FOUND")
}

// IsInvalidCredentials reports whether an error represents bad legacy login credentials.
func IsInvalidCredentials(err error) bool {
	opErr := new(OperationError)
	if !errors.As(err, &opErr) {
		return false
	}

	return strings.Contains(strings.ToUpper(opErr.Response), "INVALID_CREDENTIALS")
}

// IsInvalidSession reports whether an error represents an invalid legacy session reference.
func IsInvalidSession(err error) bool {
	opErr := new(OperationError)
	if !errors.As(err, &opErr) {
		return false
	}

	return strings.Contains(strings.ToUpper(opErr.Response), "INVALID_SESSION")
}

// IsEmailExists reports whether an error represents a duplicate legacy registration email.
func IsEmailExists(err error) bool {
	opErr := new(OperationError)
	if !errors.As(err, &opErr) {
		return false
	}

	return strings.Contains(strings.ToUpper(opErr.Response), "EMAIL_EXISTS")
}
