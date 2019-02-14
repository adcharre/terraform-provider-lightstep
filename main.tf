provider "lightstep" {
  // api_key = "${var.lighstep_api_key}"
  organization = "LightStep"
}

resource "lightstep_stream" "test_stream_1" {
  project = "saladbar-terraform"
  name = "test_stream_1"
  query = "tag:\"error\"=\"true\""
}

resource "lightstep_stream" "test_stream_2" {
  project = "saladbar-terraform"
  name = "test_stream_2"
  query = "tag:\"error\"=\"false\""
}

resource "lightstep_dashboard" "test_dashboard" {
  project = "saladbar-terraform"
  name = "test_dashboard"
  search_attributes = [{
    name = "test_stream_2",
    query = "tag:\"error\"=\"false\""
  }, {
    name = "test_stream_1",
    query = "tag:\"error\"=\"true\""
  }]
}

# resource "lightstep_project" "test_tf_project" {
#   project = "saladbar-terraform_test2"
# }
