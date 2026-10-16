environment                   = "prod"
location                      = "indonesiacentral"
shared_rg_name                = "rg-ewaste-shared"
shared_acr_name               = "acrewastescale"
shared_log_analytics_name     = "log-ewaste-centralized"
tenant_id                     = "00000000-0000-0000-0000-000000000000" # Replace/inject in pipeline
db_admin_username             = "ewasteadmin"
# image_digest should be the same approved immutable digest promoted from STG.
