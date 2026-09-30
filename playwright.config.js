import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'tests/ui',timeout:60000,use:{baseURL:process.env.DRAAS_TEST_URL||'http://127.0.0.1:8080',headless:true},reporter:[['list'],['json',{outputFile:'docs/evidence/ui-results.json'}]],workers:1});
