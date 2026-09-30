import {test,expect} from '@playwright/test';
import fs from 'node:fs';
const cfg=JSON.parse(fs.readFileSync('.local/lab-secrets.json','utf8').replace(/^\uFEFF/,''));
const auth=JSON.parse(cfg.AUTH_PRINCIPALS);
for(const [width,height] of [[1440,1000],[1024,900],[768,1024],[390,844]]){
 test(`operational dashboard ${width}x${height}`,async({page})=>{
  await page.setViewportSize({width,height});const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/');await page.getByLabel('Credencial de acesso').fill(auth[0].token);await page.getByRole('button',{name:'Entrar no laboratório'}).click();
  await expect(page.getByRole('heading',{name:'Saúde da recuperação'})).toBeVisible();await expect(page.getByText('RTO validado',{exact:true})).toBeVisible();
  await expect(page.getByText('Boot de VM',{exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBeTruthy();
  await page.screenshot({path:`docs/evidence/ui-${width}.png`,fullPage:true});
  for(const title of ['Ambientes','Workloads','Recovery points','Testes de DR','Capacidade','Auditoria','Infraestrutura','Execuções']){await page.getByRole('button',{name:title,exact:true}).click();await expect(page.getByRole('heading',{name:title,exact:true})).toBeVisible()}
  expect(errors).toEqual([]);
 })
}
test('create workload using UI and inspect persisted data',async({page})=>{await page.goto('/');await page.getByLabel('Credencial de acesso').fill(auth[0].token);await page.getByRole('button',{name:'Entrar no laboratório'}).click();await page.getByRole('button',{name:'+ Proteger workload'}).click();await expect(page.getByRole('dialog')).toBeVisible();const name=`ui-workload-${Date.now()}`;await page.getByLabel('Nome do workload').fill(name);await page.getByRole('button',{name:'Confirmar',exact:true}).click();await expect(page.getByRole('dialog')).not.toBeVisible();await page.getByRole('button',{name:'Workloads',exact:true}).click();await expect(page.getByText(name,{exact:true})).toBeVisible();await page.getByPlaceholder('Filtrar esta página…').fill('does-not-exist');await expect(page.getByText('Nenhum resultado nesta página para o filtro.')).toBeVisible()});
