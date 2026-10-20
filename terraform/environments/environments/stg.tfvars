environment                   = "stg"
location                      = "malaysiawest"
shared_rg_name                = "rg-ewastescalable-shared"
shared_acr_name               = "acrewastescalable"
shared_log_analytics_name     = "log-ewastescalable-centralized"
tenant_id                     = "00000000-0000-0000-0000-000000000000" # Replace/inject in pipeline
db_admin_username             = "ewasteadmin"
# image_digest should be the same tested immutable digest promoted from DEV.
