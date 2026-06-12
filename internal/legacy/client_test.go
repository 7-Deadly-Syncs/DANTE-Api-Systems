package legacy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetQrisMerchantBuildsSOAPRequestAndParsesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("SOAPAction"); got != "getQrisMerchant" {
			t.Fatalf("unexpected SOAPAction: %s", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}

		requestXML := string(body)
		if !strings.Contains(requestXML, `<ns:getQrisMerchant>`) {
			t.Fatalf("expected getQrisMerchant operation, got %s", requestXML)
		}
		if !strings.Contains(requestXML, `<ns:args0>MERCHANT001</ns:args0>`) {
			t.Fatalf("expected merchant code arg, got %s", requestXML)
		}

		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <ns:getQrisMerchantResponse xmlns:ns="http://BankService.services.axis2">
      <ns:return>OK|MERCHANT001|Legacy Shop</ns:return>
    </ns:getQrisMerchantResponse>
  </soapenv:Body>
</soapenv:Envelope>`))
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	merchant, err := client.GetQrisMerchant(context.Background(), "MERCHANT001")
	if err != nil {
		t.Fatalf("GetQrisMerchant returned error: %v", err)
	}

	if merchant.Code != "MERCHANT001" {
		t.Fatalf("unexpected merchant code: %s", merchant.Code)
	}
	if merchant.Name != "Legacy Shop" {
		t.Fatalf("unexpected merchant name: %s", merchant.Name)
	}
}

func TestLoginParsesSessionAndExpiry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <ns:loginResponse xmlns:ns="http://BankService.services.axis2">
      <ns:return>OK|CUST123|2623860486223779|John Doe|SESS-ABC|1718097600</ns:return>
    </ns:loginResponse>
  </soapenv:Body>
</soapenv:Envelope>`))
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	result, err := client.Login(context.Background(), "john@example.com", "secret")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}

	if result.CustomerID != "CUST123" {
		t.Fatalf("unexpected customer id: %s", result.CustomerID)
	}
	if result.SessionReference != "SESS-ABC" {
		t.Fatalf("unexpected session reference: %s", result.SessionReference)
	}

	expectedExpiry := time.Unix(1718097600, 0).UTC()
	if !result.ExpiresAt.Equal(expectedExpiry) {
		t.Fatalf("unexpected expiry: got %s want %s", result.ExpiresAt, expectedExpiry)
	}
}

func TestRegisterParsesIdentifiers(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("SOAPAction"); got != "register" {
			t.Fatalf("unexpected SOAPAction: %s", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		requestXML := string(body)
		if !strings.Contains(requestXML, `<ns:register>`) {
			t.Fatalf("expected register operation, got %s", requestXML)
		}
		if !strings.Contains(requestXML, `<ns:args1>john@example.com</ns:args1>`) {
			t.Fatalf("expected email arg, got %s", requestXML)
		}

		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <ns:registerResponse xmlns:ns="http://BankService.services.axis2">
      <ns:return>OK|CUST123|ACC987654|2623860486223779</ns:return>
    </ns:registerResponse>
  </soapenv:Body>
</soapenv:Envelope>`))
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	result, err := client.Register(context.Background(), "John Doe", "john@example.com", "secret", "123456")
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	if result.CustomerID != "CUST123" {
		t.Fatalf("unexpected customer id: %s", result.CustomerID)
	}
	if result.AccountID != "ACC987654" {
		t.Fatalf("unexpected account id: %s", result.AccountID)
	}
	if result.AccountNumber != "2623860486223779" {
		t.Fatalf("unexpected account number: %s", result.AccountNumber)
	}
}

func TestGetBalanceParsesThreeFieldLegacyResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("SOAPAction"); got != "balance" {
			t.Fatalf("unexpected SOAPAction: %s", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		requestXML := string(body)
		if !strings.Contains(requestXML, `<ns:balance>`) {
			t.Fatalf("expected balance operation, got %s", requestXML)
		}
		if !strings.Contains(requestXML, `<ns:args0>2687319122841371</ns:args0>`) {
			t.Fatalf("expected account number arg, got %s", requestXML)
		}
		if !strings.Contains(requestXML, `<ns:args1>123456</ns:args1>`) {
			t.Fatalf("expected pin arg, got %s", requestXML)
		}

		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <ns:balanceResponse xmlns:ns="http://BankService.services.axis2">
      <ns:return>OK|2687319122841371|00000000000</ns:return>
    </ns:balanceResponse>
  </soapenv:Body>
</soapenv:Envelope>`))
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	result, err := client.GetBalance(context.Background(), "2687319122841371", "123456")
	if err != nil {
		t.Fatalf("GetBalance returned error: %v", err)
	}

	if result.AccountID != "2687319122841371" {
		t.Fatalf("unexpected account id: %s", result.AccountID)
	}
	if result.ReferenceAccountID != "2687319122841371" {
		t.Fatalf("unexpected reference account id: %s", result.ReferenceAccountID)
	}
	if result.Balance != 0 {
		t.Fatalf("unexpected balance: %d", result.Balance)
	}
}

func TestPingUsesWsdl(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.RawQuery != "wsdl" {
			t.Fatalf("expected wsdl query, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
}
