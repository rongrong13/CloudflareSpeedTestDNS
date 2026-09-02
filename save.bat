@echo off
setlocal
title Export cfstd-web image

echo Exporting cfstd-web:latest to cfstd-web.tar ...
echo This may take 1-2 minutes ...
docker save -o cfstd-web.tar cfstd-web:latest
if errorlevel 1 (
    echo [ERROR] Export failed. Make sure the build succeeded first.
    pause
    exit /b 1
)

echo.
echo ============================================
echo   EXPORT SUCCESS!
echo   File: cfstd-web.tar (in this folder)
echo.
echo   Copy it to your router, then in router SSH run:
echo.
echo   docker load -i /tmp/cfstd-web.tar
echo.
echo   docker run -d --name cfstd-web --network host --restart unless-stopped -e GITHUB_TOKEN="ghp_YOUR_TOKEN" cfstd-web:latest -web
echo.
echo   Then open http://ROUTER_IP:8080
echo ============================================
pause