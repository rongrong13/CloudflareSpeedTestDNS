@echo off
setlocal
title Build cfstd-web (ARM64)

echo ============================================
echo   Build ARM64 Docker image: cfstd-web
echo ============================================
echo.

echo [CHECK] Testing Docker ...
docker version >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Docker is not running!
    echo         Please install/start Docker Desktop, then run this again.
    pause
    exit /b 1
)
echo [OK] Docker is running.
echo.

echo [CHECK] Testing buildx builder ...
docker buildx inspect arm64builder >nul 2>&1
if errorlevel 1 (
    echo [SETUP] Creating builder "arm64builder" ...
    docker buildx create --name arm64builder --use --driver docker-container
    if errorlevel 1 (
        echo [ERROR] Cannot create buildx builder.
        pause
        exit /b 1
    )
) else (
    echo [OK] Builder exists.
)
echo.

echo [BUILD] Start building ARM64 image ...
echo         First build may take 3-10 minutes.
echo         The window may look frozen - this is NORMAL.
echo         DO NOT close this window!
echo.
docker buildx build --builder arm64builder --platform linux/arm64 --progress=plain -t cfstd-web:latest --load .

if errorlevel 1 (
    echo.
    echo [ERROR] Build failed. Copy the text above and send it for help.
    pause
    exit /b 1
)

echo.
echo ============================================
echo   BUILD SUCCESS!
echo   Next step: double-click save.bat
echo ============================================
pause