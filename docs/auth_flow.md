# Auth Flow

This document defines the recommended authentication and transaction-authorization boundary for `client -> DANTE -> legacy`.

The goal is to keep DANTE as the client-facing middleware while preserving the legacy core banking system as the authority for credentials, account status, and financial-transaction approval.

## Core Principles

- `username + password` is used for `login authentication`
- `access token` / `session token` is used for authenticated access after login
- `transaction PIN` / `mPIN` is used only for `financial transaction authorization`
- DANTE should issue the client-facing session/token
- Legacy should remain the authority for credential validation, account status, and transaction approval
- Legacy session references or tokens, if they exist, should stay internal to `DANTE <-> legacy`

## Responsibility Boundary

### DANTE responsibilities

- accept and validate client requests
- enforce `rate limiting`, tracing, and audit logging
- issue and validate client-facing session/access tokens
- normalize responses and hide raw legacy internals
- orchestrate async transaction flows through queue/worker components

### Legacy responsibilities

- validate `username + password`
- validate `transaction PIN`
- return authoritative `account status`
- return authoritative `account permissions`
- execute financial transactions such as transfers and QRIS payments

## Login Flow

1. Client sends `username` and `password` to DANTE.
2. DANTE validates the request format and applies `rate limiting`, tracing, and audit logging.
3. DANTE forwards the credential-validation request to legacy.
4. Legacy validates:
   - username exists
   - password is correct
   - account or customer is active
   - channel is allowed
   - account is not blocked or restricted
5. Legacy returns the authoritative login result to DANTE.
6. DANTE issues its own client-facing session or `access token`.
7. DANTE returns the DANTE-issued token to the client.

## Post-Login Flow

After login succeeds, the client should call DANTE using the DANTE-issued token.

- `non-financial actions` should rely on the authenticated DANTE session
- `financial actions` should require `transaction PIN` in addition to the authenticated session

## Financial Transaction Flow

For transfers and QRIS payments:

1. Client sends the transaction request to DANTE with:
   - DANTE session/access token
   - transaction details
   - `transaction PIN`
2. DANTE validates the session, schema, idempotency requirements, and request metadata.
3. DANTE forwards the financial request to the legacy-backed execution flow.
4. Legacy validates:
   - account is allowed to perform the transaction
   - balance / limits / status are valid
   - `transaction PIN` is correct
   - transaction rules pass
5. Legacy executes the transaction or returns the authoritative failure result.
6. DANTE stores or propagates the outcome according to the API flow.

## Recommended Legacy Endpoints

The legacy side should expose at least these business endpoints:

- `POST /legacy/auth/login`
- `POST /legacy/auth/logout`
- `GET /legacy/accounts/{accountId}`
- `GET /legacy/accounts/{accountId}/balance`
- `POST /legacy/transfers`
- `POST /legacy/payments/qris`

## PIN Verification Best Practice

`transaction PIN` should generally be verified inside the financial transaction endpoints rather than through a separate pre-check endpoint.

Recommended pattern:

- `POST /legacy/transfers`
- `POST /legacy/payments/qris`

Reasons:

- fewer network round trips
- clearer audit trail per transaction
- tighter coupling between authorization and execution
- lower risk of detached PIN-verification state

An explicit `POST /legacy/auth/verify-transaction-pin` endpoint can exist if the architecture requires it, but it should not be the default for this project.

## Example Login Response From Legacy

```json
{
  "authenticated": true,
  "customer_id": "CIF123456",
  "account_id": "ACC987654",
  "account_status": "ACTIVE",
  "customer_name": "Budi Santoso",
  "permissions": {
    "can_view_balance": true,
    "can_transfer": true,
    "can_pay_qris": true
  },
  "legacy_session_reference": "LEG-SESSION-001",
  "expires_at": "2026-05-06T10:30:00Z"
}
```

## Notes

- Client applications should not receive raw legacy tokens unless there is a deliberate architecture decision to allow that.
- DANTE should treat any legacy session identifier as internal service data.
- Invalid credentials, blocked accounts, and legacy unavailability should map to distinct DANTE-side error handling so operational diagnosis stays clear.
