## Endpoints

### `GET /api/v1/hello`

Returns a successful Hello World response.

#### Request

No request body or parameters are required.

```
GET /api/v1/hello HTTP/1.1
Host: localhost:8080
```

#### Successful response

**Status:** `200 OK`

```
{
  "msg": "hello world"
}
```

#### PowerShell test

```
curl.exe -i http://localhost:8080/api/v1/hello
```

---

### Undefined routes

Any route that is not defined returns a standard not-found response.

#### Example request

```
GET /api/v1/unknown HTTP/1.1
Host: localhost:8080
```

#### Response

**Status:** `404 Not Found`

```
{
  "msg": "not found"
}
```

#### PowerShell test

```
curl.exe -i http://localhost:8080/api/v1/unknown
```

## Response Headers

Successful and error responses use:

```
Content-Type: application/json; charset=utf-8
```
