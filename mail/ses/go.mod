module github.com/zahansafallwa1511/go-notification/mail/ses

go 1.25

require (
	github.com/aws/aws-sdk-go-v2/service/sesv2 v1.79.8
	github.com/aws/smithy-go v1.28.8
	github.com/zahansafallwa1511/go-notification v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2 v1.47.8 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.11 // indirect
)

replace github.com/zahansafallwa1511/go-notification => ../..
