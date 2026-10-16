#!/usr/bin/env python3
"""
Single Public Entry & Path Routing Contract Verifier (Decision AD4 / EWSS-29).
Validates:
1. All route rewrite destination targets are defined in src/frontend/next.config.ts.
2. The UI container app is configured with external_enabled = true (Public Ingress).
3. All internal microservices (auth, batch/api, claim, custody, analytics) have external_enabled = false.
4. UI container app receives both canonical and alias service URLs.
"""

import os
import re
import sys

def verify_contracts():
    root_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    next_config_path = os.path.join(root_dir, "src", "frontend", "next.config.ts")
    main_tf_path = os.path.join(root_dir, "terraform", "environments", "main.tf")

    passed = 0
    failed = 0

    print("==============================================================================")
    print("   SINGLE PUBLIC ENTRY (AD4) CONTRACT VERIFICATION (EWSS-29)")
    print("==============================================================================")

    # 1. Next.js Routing
    print("\n--- Check 1: Next.js Path Routing & Rewrites ---")
    if not os.path.exists(next_config_path):
        print(f"[FAIL] Missing {next_config_path}")
        return 1

    with open(next_config_path, "r", encoding="utf-8") as f:
        next_config_src = f.read()

    expected_routes = [
        ("v1/auth", "Identity & Access Service"),
        ("v1/claims/batches", "Claim Logistics Service (Claims)"),
        ("v1/logistics/assignments", "Claim Logistics Service (Assignments)"),
        ("v1/custody/assignments", "Custody & Verification Service (Handoff)"),
        ("v1/processing/batches", "Custody & Verification Service (Receipt/Treatment)"),
        ("v1/evidence/batches", "Custody & Verification Service (Evidence)"),
        ("v1/processing", "Custody & Verification Service (Processing)"),
        ("v1/audit", "Custody & Verification Service (Audit)"),
        ("internal/v1/analytics/batches", "Batch Workflow Service (Analytics Facade)"),
        ("v1/opportunities", "Batch Workflow Service (Opportunities)"),
        ("v1/batches", "Batch Workflow Service (Batches)"),
    ]

    for route, desc in expected_routes:
        if route in next_config_src:
            print(f"[PASS] Next.js rewrite target verified: {route:30} -> {desc}")
            passed += 1
        else:
            print(f"[FAIL] Next.js missing rewrite target: {route}")
            failed += 1

    # 2. Terraform Ingress Security Configuration
    print("\n--- Check 2: Terraform Ingress Security Posture ---")
    if not os.path.exists(main_tf_path):
        print(f"[FAIL] Missing {main_tf_path}")
        return 1

    with open(main_tf_path, "r", encoding="utf-8") as f:
        tf_src = f.read()

    # Find resource blocks
    resource_pattern = re.compile(
        r'resource\s+"azurerm_container_app"\s+"([^"]+)"\s*\{(.*?)\n\}',
        re.DOTALL
    )

    resources = {}
    for match in resource_pattern.finditer(tf_src):
        app_name = match.group(1)
        body = match.group(2)
        # Find ingress block inside this container app
        ingress_match = re.search(r'ingress\s*\{(.*?)\}', body, re.DOTALL)
        if ingress_match:
            ingress_body = ingress_match.group(1)
            ext_match = re.search(r'external_enabled\s*=\s*([^\s\n#]+)', ingress_body)
            ext_val = ext_match.group(1) if ext_match else "missing"
            resources[app_name] = ext_val
        else:
            resources[app_name] = "no_ingress"

    # UI must have external_enabled = true
    ui_ingress = resources.get("ui")
    if ui_ingress == "true":
        print(f"[PASS] Single Public Entry [UI]: external_enabled = true (Public Ingress on Port 3000)")
        passed += 1
    else:
        print(f"[FAIL] Single Public Entry [UI] has invalid ingress: {ui_ingress}")
        failed += 1

    # Backend microservices must have external_enabled = false
    internal_services = [
        ("auth", "Identity & Access API"),
        ("api", "Batch Workflow API"),
        ("claim", "Claim Logistics API"),
        ("custody", "Custody & Verification API"),
        ("analytics", "Analytics Worker"),
    ]

    for svc_key, svc_desc in internal_services:
        val = resources.get(svc_key)
        if val == "false":
            print(f"[PASS] Internal Service [{svc_desc}]: external_enabled = false (Public Access CLOSED)")
            passed += 1
        else:
            print(f"[FAIL] Internal Service [{svc_desc}] has invalid ingress: {val}")
            failed += 1

    # Matching worker should have no ingress
    mw_val = resources.get("matching_worker")
    if mw_val == "no_ingress":
        print(f"[PASS] Headless Worker [Matching Worker]: No public ingress defined (Isolated Worker)")
        passed += 1
    else:
        print(f"[FAIL] Headless Worker has unexpected ingress: {mw_val}")
        failed += 1

    # Check UI canonical env vars
    print("\n--- Check 3: UI Container App Service URL Injections ---")
    ui_body_match = re.search(r'resource\s+"azurerm_container_app"\s+"ui"\s*\{(.*?)\n\}', tf_src, re.DOTALL)
    if ui_body_match:
        ui_body = ui_body_match.group(1)
        required_env_vars = [
            "IDENTITY_ACCESS_URL",
            "BATCH_WORKFLOW_URL",
            "CLAIM_LOGISTICS_URL",
            "CUSTODY_VERIFICATION_URL",
            "AUTH_SERVICE_URL",
            "BATCH_SERVICE_URL",
            "CLAIM_SERVICE_URL",
            "CUSTODY_SERVICE_URL",
        ]
        for var_name in required_env_vars:
            if f'name  = "{var_name}"' in ui_body or f'name = "{var_name}"' in ui_body:
                print(f"[PASS] UI environment variable configured: {var_name}")
                passed += 1
            else:
                print(f"[FAIL] UI missing environment variable: {var_name}")
                failed += 1

    print("\n==============================================================================")
    print(f"Contract Verification Summary: {passed} passed, {failed} failed")
    print("==============================================================================")

    return 0 if failed == 0 else 1

if __name__ == "__main__":
    sys.exit(verify_contracts())
