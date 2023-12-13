$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -tags lambda.norpc -o ./build/bootstrap main.go
~\Go\Bin\build-lambda-zip.exe -o ./build/lambda_handler.zip ./build/bootstrap