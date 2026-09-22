$ErrorActionPreference = 'Stop'

# Keep the standalone module independent from a parent go.work file and make
# the target explicit so a Windows build cannot be uploaded as the server binary.
$env:GOWORK = 'off'
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }

$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -trimpath -o natbox-linux-amd64 .
if ($LASTEXITCODE -ne 0) { throw 'natbox build failed' }
go build -trimpath -o natbox-hash-linux-amd64 ./cmd/natbox-hash
if ($LASTEXITCODE -ne 0) { throw 'natbox-hash build failed' }

Get-FileHash -Algorithm SHA256 natbox-linux-amd64, natbox-hash-linux-amd64 |
  ForEach-Object { '{0}  {1}' -f $_.Hash.ToLowerInvariant(), $_.Path.Substring((Get-Location).Path.Length + 1) } |
  Set-Content -Encoding ascii SHA256SUMS

Write-Host 'Linux amd64 artifacts are ready:'
Get-Item natbox-linux-amd64, natbox-hash-linux-amd64, SHA256SUMS |
  Select-Object Name, Length
