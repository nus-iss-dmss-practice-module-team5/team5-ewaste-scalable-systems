-- Runner must fail when any result is FAIL; see scripts/test-persistence.sh.
SELECT 'table.batch_evidence' test_name,IF((SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_evidence')='1','PASS','FAIL') result
UNION ALL
SELECT 'table.batch_receipts' test_name,IF((SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_receipts')='1','PASS','FAIL') result
UNION ALL
SELECT 'table.batch_treatments' test_name,IF((SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_treatments')='1','PASS','FAIL') result
UNION ALL
SELECT 'table.batch_impact_metrics' test_name,IF((SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_impact_metrics')='1','PASS','FAIL') result
UNION ALL
SELECT 'table.batch_anomalies' test_name,IF((SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_anomalies')='1','PASS','FAIL') result
UNION ALL
SELECT 'index.uq_receipt_batch' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_receipts' AND index_name='uq_receipt_batch' AND non_unique=0)='batch_id','PASS','FAIL') result
UNION ALL
SELECT 'index.uq_receipt_command' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_receipts' AND index_name='uq_receipt_command' AND non_unique=0)='command_id','PASS','FAIL') result
UNION ALL
SELECT 'index.uq_treatment_batch' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_treatments' AND index_name='uq_treatment_batch' AND non_unique=0)='batch_id','PASS','FAIL') result
UNION ALL
SELECT 'index.uq_impact_source_event' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_impact_metrics' AND index_name='uq_impact_source_event' AND non_unique=0)='source_event_id','PASS','FAIL') result
UNION ALL
SELECT 'index.uq_anomaly_result_code' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_anomalies' AND index_name='uq_anomaly_result_code' AND non_unique=0)='metric_id,anomaly_code','PASS','FAIL') result
UNION ALL
SELECT 'index.idx_receipt_facility' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_receipts' AND index_name='idx_receipt_facility' AND non_unique=1)='facility_org_id,verified_at','PASS','FAIL') result
UNION ALL
SELECT 'index.idx_treatment_facility' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_treatments' AND index_name='idx_treatment_facility' AND non_unique=1)='facility_org_id,completed_at','PASS','FAIL') result
UNION ALL
SELECT 'index.idx_impact_quality' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_impact_metrics' AND index_name='idx_impact_quality' AND non_unique=1)='data_quality,calculated_at','PASS','FAIL') result
UNION ALL
SELECT 'index.idx_anomalies_batch' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_anomalies' AND index_name='idx_anomalies_batch' AND non_unique=1)='batch_id,anomaly_code','PASS','FAIL') result
UNION ALL
SELECT 'index.uq_evidence_scope' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_evidence' AND index_name='uq_evidence_scope' AND non_unique=0)='evidence_id,batch_id,organisation_id,lifecycle_stage','PASS','FAIL') result
UNION ALL
SELECT 'fk.fk_receipt_command_batch' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='batch_receipts' AND constraint_name='fk_receipt_command_batch' AND referenced_table_name='command_idempotency')='command_id,batch_id','PASS','FAIL') result
UNION ALL
SELECT 'fk.fk_treatment_receipt' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='batch_treatments' AND constraint_name='fk_treatment_receipt' AND referenced_table_name='batch_receipts')='receipt_id,batch_id,facility_org_id,receipt_version,received_weight_kg','PASS','FAIL') result
UNION ALL
SELECT 'fk.fk_treatment_evidence' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='batch_treatments' AND constraint_name='fk_treatment_evidence' AND referenced_table_name='batch_evidence')='evidence_id,batch_id,facility_org_id,evidence_stage','PASS','FAIL') result
UNION ALL
SELECT 'fk.fk_impact_source_event' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='batch_impact_metrics' AND constraint_name='fk_impact_source_event' AND referenced_table_name='event_outbox')='source_event_id,batch_id','PASS','FAIL') result
UNION ALL
SELECT 'fk.fk_impact_treatment' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='batch_impact_metrics' AND constraint_name='fk_impact_treatment' AND referenced_table_name='batch_treatments')='treatment_id,batch_id,facility_org_id,treatment_version','PASS','FAIL') result
UNION ALL
SELECT 'fk.fk_anomaly_result' test_name,IF((SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='batch_anomalies' AND constraint_name='fk_anomaly_result' AND referenced_table_name='batch_impact_metrics')='metric_id,batch_id','PASS','FAIL') result
UNION ALL
SELECT 'liquibase.processing' test_name,IF((SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID IN ('EWCSB3-026','EWCSB3-027','EWCSB3-028','EWCSB3-029','EWCSB3-030'))='5','PASS','FAIL') result
ORDER BY test_name;
