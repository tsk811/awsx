package main

type awsRegion struct {
	code string
	name string
}

// Commercial regions, sorted alphabetically by code. Updated from:
// https://docs.aws.amazon.com/global-infrastructure/latest/regions/aws-regions.html
// Region discovery deliberately requires no AWS access.
var regions = []awsRegion{
	{code: "af-south-1", name: "Africa (Cape Town)"},
	{code: "ap-east-1", name: "Asia Pacific (Hong Kong)"},
	{code: "ap-east-2", name: "Asia Pacific (Taipei)"},
	{code: "ap-northeast-1", name: "Asia Pacific (Tokyo)"},
	{code: "ap-northeast-2", name: "Asia Pacific (Seoul)"},
	{code: "ap-northeast-3", name: "Asia Pacific (Osaka)"},
	{code: "ap-south-1", name: "Asia Pacific (Mumbai)"},
	{code: "ap-south-2", name: "Asia Pacific (Hyderabad)"},
	{code: "ap-southeast-1", name: "Asia Pacific (Singapore)"},
	{code: "ap-southeast-2", name: "Asia Pacific (Sydney)"},
	{code: "ap-southeast-3", name: "Asia Pacific (Jakarta)"},
	{code: "ap-southeast-4", name: "Asia Pacific (Melbourne)"},
	{code: "ap-southeast-5", name: "Asia Pacific (Malaysia)"},
	{code: "ap-southeast-6", name: "Asia Pacific (New Zealand)"},
	{code: "ap-southeast-7", name: "Asia Pacific (Thailand)"},
	{code: "ca-central-1", name: "Canada (Central)"},
	{code: "ca-west-1", name: "Canada West (Calgary)"},
	{code: "eu-central-1", name: "Europe (Frankfurt)"},
	{code: "eu-central-2", name: "Europe (Zurich)"},
	{code: "eu-north-1", name: "Europe (Stockholm)"},
	{code: "eu-south-1", name: "Europe (Milan)"},
	{code: "eu-south-2", name: "Europe (Spain)"},
	{code: "eu-west-1", name: "Europe (Ireland)"},
	{code: "eu-west-2", name: "Europe (London)"},
	{code: "eu-west-3", name: "Europe (Paris)"},
	{code: "il-central-1", name: "Israel (Tel Aviv)"},
	{code: "me-central-1", name: "Middle East (UAE)"},
	{code: "me-south-1", name: "Middle East (Bahrain)"},
	{code: "mx-central-1", name: "Mexico (Central)"},
	{code: "sa-east-1", name: "South America (São Paulo)"},
	{code: "us-east-1", name: "US East (N. Virginia)"},
	{code: "us-east-2", name: "US East (Ohio)"},
	{code: "us-west-1", name: "US West (N. California)"},
	{code: "us-west-2", name: "US West (Oregon)"},
}

func isRegion(value string) bool {
	for _, region := range regions {
		if region.code == value {
			return true
		}
	}
	return false
}
